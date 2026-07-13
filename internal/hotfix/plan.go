// Package hotfix plans and validates per-environment hotfixes that apply a
// trunk commit onto an environment pinned to an older trunk base.
//
// The plan verb computes and validates only; the cherry-pick, build, deploy,
// and state write happen in the generated workflow. A plan with --dry-run
// mutates nothing.
package hotfix

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/taggrammar"
	"github.com/stablekernel/cascade/internal/version"
)

// defaultRemote is the git remote env branches live on.
const defaultRemote = "origin"

// hotfixPRLabel is the label that identifies an in-flight hotfix resolution PR.
//
// The literal must stay in sync with hotfixLabel in
// internal/generate/hotfix.go, which seeds and applies the same label in the
// generated workflow. The two live in separate packages without a shared
// constant.
const hotfixPRLabel = "cascade-hotfix"

// hotfixConflictPRLabel is the label applied to a hotfix resolution PR that
// carries cherry-pick conflicts for a human to resolve.
//
// The literal must stay in sync with hotfixConflictLabel in
// internal/generate/hotfix.go.
const hotfixConflictPRLabel = "cascade-hotfix-conflict"

// OpenPR is a minimal view of an open pull request returned by a PRChecker.
type OpenPR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// PRChecker reports open hotfix PRs targeting a given base branch. It is the
// single-flight gate: the plan verb refuses to proceed while a hotfix PR is
// already open against the target env branch. The default implementation is a
// no-op that reports no open PRs, so callers without GitHub context (and unit
// tests) are not forced to provide one.
type PRChecker interface {
	// OpenHotfixPRs returns open PRs labeled cascade-hotfix whose base is baseBranch.
	OpenHotfixPRs(baseBranch string) ([]OpenPR, error)
}

// noopPRChecker reports no open PRs.
type noopPRChecker struct{}

func (noopPRChecker) OpenHotfixPRs(string) ([]OpenPR, error) { return nil, nil }

// GitRunner abstracts the few git operations the planner performs so tests can
// observe, so dry-run can suppress mutation, and so a caller without a git
// checkout (the what-if simulator) can inject a record-only implementation via
// WithGitRunner. The default implementation shells out to git in the current
// working directory.
type GitRunner interface {
	// ResolveSHA resolves a ref or short SHA to a full commit SHA.
	ResolveSHA(ref string) (string, error)
	// LocalBranchExists reports whether a local branch exists.
	LocalBranchExists(name string) (bool, error)
	// LocalBranchSHA returns the tip SHA of a local branch.
	LocalBranchSHA(name string) (string, error)
	// RemoteBranchSHA returns the tip SHA of a remote-tracking branch
	// (refs/remotes/<remote>/<name>) and whether that ref exists. A missing ref
	// returns ("", false, nil); only an unexpected git failure returns an error.
	RemoteBranchSHA(remote, name string) (string, bool, error)
	// CreateBranch creates a branch pointing at sha.
	CreateBranch(name, sha string) error
	// ResetBranch force-updates the remote branch <name> on <remote> to point at
	// sha, then best-effort aligns the local branch of the same name. It is the
	// self-heal primitive: it rewrites history on the env branch, so callers must
	// gate it behind the orphan-safety rule (see reconcileBranch).
	ResetBranch(remote, name, sha string) error
}

type execGitRunner struct{}

func (execGitRunner) ResolveSHA(ref string) (string, error) {
	out, err := exec.Command("git", "rev-parse", "--verify", ref+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (execGitRunner) LocalBranchExists(name string) (bool, error) {
	err := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+name).Run()
	if err == nil {
		return true, nil
	}
	if _, ok := err.(*exec.ExitError); ok {
		return false, nil
	}
	return false, fmt.Errorf("git rev-parse refs/heads/%s: %w", name, err)
}

func (execGitRunner) LocalBranchSHA(name string) (string, error) {
	out, err := exec.Command("git", "rev-parse", "refs/heads/"+name).Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse refs/heads/%s: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (execGitRunner) RemoteBranchSHA(remote, name string) (string, bool, error) {
	ref := "refs/remotes/" + remote + "/" + name
	out, err := exec.Command("git", "rev-parse", "--verify", "--quiet", ref).Output()
	if err != nil {
		// rev-parse --quiet exits non-zero with no output when the ref is
		// absent; treat that as "remote branch not fetched", not a hard failure.
		if _, ok := err.(*exec.ExitError); ok {
			return "", false, nil
		}
		return "", false, fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), true, nil
}

func (execGitRunner) CreateBranch(name, sha string) error {
	if out, err := exec.Command("git", "branch", name, sha).CombinedOutput(); err != nil {
		return fmt.Errorf("git branch %s %s: %w\n%s", name, sha, err, out)
	}
	return nil
}

func (execGitRunner) ResetBranch(remote, name, sha string) error {
	// Force-update the remote ref first: the remote env branch is the one the
	// apply job opens its resolution PR against, so it must carry the corrected
	// base even if aligning the local branch later fails.
	pushRef := sha + ":refs/heads/" + name
	if out, err := exec.Command("git", "push", "--force", remote, pushRef).CombinedOutput(); err != nil {
		return fmt.Errorf("git push --force %s %s: %w\n%s", remote, pushRef, err, out)
	}
	// Align the local branch best-effort. -f creates it when absent, which is
	// harmless: the local ref is only a convenience for subsequent local steps.
	if out, err := exec.Command("git", "branch", "-f", name, sha).CombinedOutput(); err != nil {
		return fmt.Errorf("git branch -f %s %s: %w\n%s", name, sha, err, out)
	}
	return nil
}

// Planner validates and computes a hotfix plan for one environment.
type Planner struct {
	cicd      *config.CICDFile
	actor     string
	dryRun    bool
	remote    string
	prChecker PRChecker
	// realPRChecker is true only when a non-nil PRChecker was injected via
	// WithPRChecker. It distinguishes a genuine single-flight lookup from the
	// no-op default, and gates orphan self-heal: the planner resets an env branch
	// only when a real checker actually proved no resolution PR is open.
	realPRChecker bool
	gitRunner     GitRunner
	// ancestor resolves whether one commit is an ancestor of another. It is the
	// seam the trunk-ancestry gate and the per-(commit,env) presence check run
	// through, so a caller without a git checkout (the what-if simulator) can
	// inject a record-only ancestry oracle via WithAncestorChecker. The default
	// is git.IsAncestor, so production behavior is unchanged.
	ancestor AncestorChecker
	// component, when non-empty, names the declared component this hotfix is
	// scoped to. It is set only via WithPlanComponent by a per-component generated
	// hotfix workflow. An empty value selects the single-component path, whose
	// integration branch is env/<env>; a named component names the integration
	// branch env/<component>/<env> so each component's branches occupy a disjoint
	// namespace and agree with the component-aware finalize path.
	component string
}

// PlannerOptions carries the required inputs for NewPlanner.
type PlannerOptions struct {
	ConfigPath  string
	ManifestKey string
	Actor       string
}

// AncestorChecker reports whether ancestor is an ancestor of descendant. It is
// the seam the trunk-ancestry gate and the per-(commit,env) presence check run
// through. The default is git.IsAncestor; the what-if simulator injects a
// record-only oracle so PlanChain can compute the cherry-pick chain without a
// git checkout.
type AncestorChecker func(ancestor, descendant string) (bool, error)

// Option configures optional, additive Planner behavior.
type Option func(*Planner)

// WithGitRunner injects the git operations the planner performs. The default
// shells out to git in the current working directory; a caller without a
// checkout (the what-if simulator) supplies a record-only implementation. A nil
// runner is ignored, keeping the default.
func WithGitRunner(r GitRunner) Option {
	return func(p *Planner) {
		if r != nil {
			p.gitRunner = r
		}
	}
}

// WithAncestorChecker injects the ancestry oracle the trunk-ancestry gate and
// the commit-presence check consult. The default is git.IsAncestor, so the real
// hotfix path is unchanged; a caller without a checkout (the what-if simulator)
// supplies a record-only checker. A nil checker is ignored, keeping the default.
func WithAncestorChecker(fn AncestorChecker) Option {
	return func(p *Planner) {
		if fn != nil {
			p.ancestor = fn
		}
	}
}

// WithDryRun controls whether the planner mutates anything. When true the env
// branch is computed but not created.
func WithDryRun(dryRun bool) Option {
	return func(p *Planner) { p.dryRun = dryRun }
}

// WithPRChecker injects the single-flight PR lookup. The default reports no
// open PRs.
func WithPRChecker(c PRChecker) Option {
	return func(p *Planner) {
		if c != nil {
			p.prChecker = c
			p.realPRChecker = true
		}
	}
}

// WithRemote overrides the git remote env branches live on (default "origin").
func WithRemote(remote string) Option {
	return func(p *Planner) {
		if remote != "" {
			p.remote = remote
		}
	}
}

// WithPlanComponent scopes the plan to a declared component. It names the
// integration branch env/<component>/<env> so the plan agrees with the
// component-aware finalize path and each component's branches occupy a disjoint
// namespace. An empty name (the default) keeps the single-component behavior
// byte-identical, naming the branch env/<env>. This mirrors the finalize
// package option WithComponent; the two names differ only because the plan and
// finalize option types are distinct.
func WithPlanComponent(name string) Option {
	return func(p *Planner) { p.component = name }
}

// NewPlanner constructs a Planner from the manifest at opts.ConfigPath.
func NewPlanner(opts PlannerOptions, options ...Option) (*Planner, error) {
	key := opts.ManifestKey
	if key == "" {
		key = config.DefaultManifestKey
	}

	// Read raw bytes rather than delegating straight to config.ParseManifestFile
	// so a component-scoped planner can overlay state.components.<component>.<env>
	// below; ParseManifestFile alone discards the bytes needed for that read.
	raw, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("reading manifest file: %w", err)
	}
	cicd, err := config.ParseManifestBytes(raw, key)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	if cicd.Config != nil {
		cicd.Config.ManifestFile = opts.ConfigPath
	}

	actor := opts.Actor
	if actor == "" {
		actor = "github-actions[bot]"
	}

	p := &Planner{
		cicd:      cicd,
		actor:     actor,
		remote:    defaultRemote,
		prChecker: noopPRChecker{},
		gitRunner: execGitRunner{},
		ancestor:  git.IsAncestor,
	}
	for _, o := range options {
		o(p)
	}

	// A component-scoped plan reads its prior env state from
	// state.components.<component>.<env>, not the flat state.<env> node (which a
	// multi-component manifest never populates for a declared component). Overlay
	// it into the flat map so Plan and PlanChain's State[env] lookups transparently
	// see the component's recorded row, mirroring the finalize path's
	// overlayComponentState. A no-op for the single-component default.
	if p.cicd.State == nil {
		p.cicd.State = make(map[string]*config.EnvState)
	}
	if err := overlayComponentState(p.cicd, raw, key, p.component); err != nil {
		return nil, err
	}

	return p, nil
}

// PlanResult is the computed, validated hotfix plan emitted as JSON and GHA
// outputs. It records only what the workflow needs; no mutation has happened
// beyond the optional env-branch creation.
type PlanResult struct {
	TargetEnv string `json:"target_env"`
	FixSHA    string `json:"fix_sha"`
	Branch    string `json:"branch"`
	BaseSHA   string `json:"base_sha"`

	// NoOp is true when the fix is already contained in the target state SHA.
	NoOp bool `json:"no_op"`

	// BranchCreated is true when env/<target> was (or, in dry-run, would be)
	// created at BaseSHA. False when it already existed at the expected tip.
	BranchCreated bool `json:"branch_created"`

	// BranchReset is true when env/<target> existed at a stale tip but was (or, in
	// dry-run, would be) force-reset back to BaseSHA by the orphan self-heal. It is
	// set only when the env is not diverged and the single-flight gate ran with a
	// real checker that found no open resolution PR.
	BranchReset bool `json:"branch_reset"`

	// HotfixVersionCandidate is the next free hotfix version over the target
	// env's current version base (e.g. v1.0.0-rc.1 -> v1.0.0-rc.1.hotfix.1).
	HotfixVersionCandidate string `json:"hotfix_version_candidate"`

	// ConflictExpected hints whether the cherry-pick is likely to conflict.
	// The plan verb does not run the cherry-pick, so this is best-effort and
	// false by default; the workflow is authoritative.
	ConflictExpected bool `json:"conflict_expected"`

	// ProtectionSuggestions are ready-to-run gh/gh api commands an operator can
	// paste to establish env/* branch protection. cascade never applies these.
	ProtectionSuggestions []string `json:"protection_suggestions"`

	DryRun bool `json:"dry_run"`
}

// Plan validates the hotfix request and computes the env-branch plan.
//
// fixRef is the trunk commit (or ref/short SHA) to apply; targetEnv is the
// environment to hotfix. It enforces, in order: trunk ancestry of the fix,
// target-env eligibility, no-op detection, the single-flight open-PR gate, and
// env-branch reconciliation. The single-flight gate runs before any branch
// mutation, so a blocked plan leaves no git state changes.
func (p *Planner) Plan(fixRef, targetEnv string) (*PlanResult, error) {
	cfg := p.cicd.Config
	if cfg == nil {
		return nil, fmt.Errorf("manifest has no config block")
	}

	// 1. Resolve the fix and enforce the trunk-ancestry gate over the (single)
	// requested ref. resolveTrunkAncestors handles resolution and the per-ref
	// ancestry check so the single-env and chain paths share one gate.
	shas, err := p.resolveTrunkAncestors([]string{fixRef})
	if err != nil {
		return nil, err
	}
	fixSHA := shas[0]

	// 2. Target-env eligibility.
	if cfg.GetEnvironmentIndex(targetEnv) == -1 {
		return nil, fmt.Errorf("%q is not a configured environment", targetEnv)
	}
	if cfg.IsFirstEnvironment(targetEnv) {
		return nil, fmt.Errorf("%q is the first environment; a fix reaches it by merging to trunk, not by hotfix", targetEnv)
	}
	// Prod IS eligible here; prod gating happens at the workflow layer.

	state := p.cicd.State[targetEnv]
	if state == nil || state.SHA == "" {
		return nil, fmt.Errorf("environment %q has no recorded state SHA", targetEnv)
	}
	baseSHA := state.SHA

	branch := p.envBranch(targetEnv)
	result := &PlanResult{
		TargetEnv:             targetEnv,
		FixSHA:                fixSHA,
		Branch:                branch,
		BaseSHA:               baseSHA,
		ProtectionSuggestions: protectionSuggestions(branch),
		DryRun:                p.dryRun,
	}

	// 3. No-op check: the fix is already present in the target when it is an
	// ancestor of the recorded state SHA OR it is already recorded in the env's
	// applied-patch list. Consulting state.Patches closes the gap where a prior
	// hotfix applied the commit onto the diverged env branch without advancing
	// state.SHA, which a pure ancestry check would miss and replan.
	already, err := p.commitPresentInEnv(fixSHA, baseSHA, state.Patches)
	if err != nil {
		return nil, fmt.Errorf("checking whether fix is already in %q: %w", targetEnv, err)
	}
	if already {
		result.NoOp = true
		return result, nil
	}

	// Compute the hotfix version candidate from the env's current version.
	candidate, err := hotfixVersionCandidate(resolveTagGrammar(p.cicd), state.Version)
	if err != nil {
		return nil, err
	}
	result.HotfixVersionCandidate = candidate

	// 4. Single-flight: refuse if a hotfix PR already targets env/<target>. This
	// runs before any branch mutation so a blocked plan leaves no git state.
	// singleFlightChecked is true only when a real (non-no-op) checker confirmed
	// no open resolution PR; it is the precondition for orphan self-heal.
	singleFlightChecked, err := p.checkSingleFlight(branch)
	if err != nil {
		return nil, err
	}

	// 5. env/<target> branch reconciliation. Only after the single-flight gate
	// passes do we create, validate, or self-heal the env branch.
	diverged := state.IsDiverged()
	created, reset, err := p.reconcileBranch(branch, baseSHA, diverged, singleFlightChecked)
	if err != nil {
		return nil, err
	}
	result.BranchCreated = created
	result.BranchReset = reset

	return result, nil
}

// checkSingleFlight runs the single-flight open-PR gate for branch. It returns
// the same abort error as before when a hotfix PR is already open against the
// branch. Otherwise it returns whether a real checker ran: true only when a
// non-no-op PRChecker was injected, which is the precondition the orphan
// self-heal requires before it may reset an env branch.
func (p *Planner) checkSingleFlight(branch string) (checked bool, err error) {
	openPRs, err := p.prChecker.OpenHotfixPRs(branch)
	if err != nil {
		return false, fmt.Errorf("checking for open hotfix PRs: %w", err)
	}
	if len(openPRs) > 0 {
		pr := openPRs[0]
		return false, fmt.Errorf("a hotfix PR (#%d %s) labeled %q already targets %s; resolve and finalize it, then re-dispatch this hotfix",
			pr.Number, pr.URL, hotfixPRLabel, branch)
	}
	return p.realPRChecker, nil
}

// reconcileBranch ensures env/<target> is anchored at baseSHA. If absent it is
// created at baseSHA (unless dry-run, where creation is only reported). If
// present and already at baseSHA it is left untouched. If present at a stale tip
// it is either self-healed back to baseSHA or the run aborts fail-closed.
//
// Self-heal safety rule: divergence is recorded ONLY at hotfix finalize, so a
// live in-flight hotfix (open resolution PR, real work on env/<env>) reports
// !diverged while its branch legitimately leads baseSHA. Force-resetting that
// would destroy work. The reset is therefore safe ONLY when the env is not
// diverged AND the single-flight gate actually ran with a real checker that
// found no open hotfix PR. That intersection is exactly the OrphanEnvBranches
// population (an env/* branch with no diverged env behind it) proven not
// in-flight: an abandoned hotfix branch left by an interrupted run. In every
// other mismatch case the planner stays fail-closed with envTipDivergenceError.
//
// Returns whether the branch was (or would be) created and whether it was (or
// would be) reset; at most one of the two is true.
func (p *Planner) reconcileBranch(branch, baseSHA string, diverged, singleFlightChecked bool) (created, reset bool, err error) {
	exists, err := p.gitRunner.LocalBranchExists(branch)
	if err != nil {
		return false, false, fmt.Errorf("checking branch %s: %w", branch, err)
	}

	if exists {
		tip, err := p.gitRunner.LocalBranchSHA(branch)
		if err != nil {
			return false, false, fmt.Errorf("reading tip of %s: %w", branch, err)
		}
		if tip != baseSHA {
			if !diverged && singleFlightChecked {
				if p.dryRun {
					return false, true, nil
				}
				if err := p.gitRunner.ResetBranch(p.remote, branch, baseSHA); err != nil {
					return false, false, fmt.Errorf("self-healing orphan %s to %s: %w", branch, short(baseSHA), err)
				}
				return false, true, nil
			}
			return false, false, envTipDivergenceError(branch, tip, baseSHA, diverged)
		}
		return false, false, nil
	}

	// Branch absent: it will be created at baseSHA.
	if p.dryRun {
		return true, false, nil
	}
	if err := p.gitRunner.CreateBranch(branch, baseSHA); err != nil {
		return false, false, fmt.Errorf("creating %s at %s: %w", branch, short(baseSHA), err)
	}
	return true, false, nil
}

// envTipDivergenceError reports that an env branch tip no longer matches the
// recorded state SHA the cherry-pick base is derived from. It names the env
// branch and both SHAs and gives an actionable recovery path that depends on
// whether the environment has recorded divergence. Both the local-branch guard
// (reconcileBranch) and the chain path's remote-tip guard (verifyRemoteEnvTip)
// raise this single message so the two divergence checks stay in lockstep.
//
// diverged true means the environment carries an in-progress hotfix (divergence
// was recorded at finalize), so the branch tip leads the recorded base on
// purpose and resetting it could discard real work. diverged false means no
// divergence is recorded, so the branch is an abandoned hotfix branch that
// cascade can self-heal once a real single-flight check confirms no resolution
// PR is open.
func envTipDivergenceError(branch, tip, baseSHA string, diverged bool) error {
	if diverged {
		return fmt.Errorf(
			"branch %s tip %s does not match recorded state SHA %s. This environment carries an in-progress hotfix with recorded divergence, so the branch leads the recorded base on purpose. To recover, finalize or abandon the open resolution PR, or reset %s to %s only after confirming no work is lost, then re-run",
			branch, short(tip), short(baseSHA), branch, short(baseSHA))
	}
	return fmt.Errorf(
		"branch %s tip %s does not match recorded state SHA %s, and no divergence is recorded, so %s is an abandoned hotfix branch. Re-run the hotfix plan with --repo set so cascade can confirm no resolution PR is open and self-heal the branch, or remove it manually by resetting %s to %s. Run cascade status consistency to list orphan env branches",
		branch, short(tip), short(baseSHA), branch, branch, short(baseSHA))
}

// hotfixVersionCandidate returns the next free hotfix version over the base of
// envVersion under spec. A pre-release version yields its first nested hotfix
// segment, rendered in the configured grammar.
func hotfixVersionCandidate(spec taggrammar.Spec, envVersion string) (string, error) {
	if envVersion == "" {
		return "", fmt.Errorf("target environment has no recorded version; cannot compute hotfix version")
	}
	v, err := version.ParseWithGrammar(spec, envVersion)
	if err != nil {
		return "", fmt.Errorf("parsing target version %q: %w", envVersion, err)
	}
	return v.WithGrammar(spec).NextHotfix().String(), nil
}

// envBranch returns the integration branch name for env under this planner's
// component. The default (empty) component yields env/<env>, byte-identical to
// the historical single-component name; a named component yields
// env/<component>/<env> so the plan agrees with the component-aware finalize
// path and each component's integration branches occupy a disjoint namespace.
func (p *Planner) envBranch(env string) string {
	return EnvBranchName(p.component, env)
}

// protectionSuggestions returns ready-to-run gh CLI commands an operator can
// paste to protect the env branch. cascade only prints these; it never applies
// branch protection itself.
func protectionSuggestions(branch string) []string {
	return []string{
		fmt.Sprintf("# Protect %s so hotfix resolution PRs merge through review:", branch),
		fmt.Sprintf("gh api -X PUT repos/{owner}/{repo}/branches/%s/protection "+
			"-f required_pull_request_reviews.required_approving_review_count=1 "+
			"-F enforce_admins=true "+
			"-F required_status_checks=null "+
			"-F restrictions=null", branch),
		fmt.Sprintf("gh label create %s --color B60205 --description \"Cascade hotfix resolution PR\" || true", hotfixPRLabel),
		fmt.Sprintf("gh label create %s --color D93F0B --description \"Cascade hotfix resolution PR with cherry-pick conflicts\" || true", hotfixConflictPRLabel),
	}
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
