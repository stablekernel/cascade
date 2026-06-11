// Package hotfix plans and validates per-environment hotfixes that apply a
// trunk commit onto an environment pinned to an older trunk base.
//
// The plan verb computes and validates only; the cherry-pick, build, deploy,
// and state write happen in the generated workflow. A plan with --dry-run
// mutates nothing.
package hotfix

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/version"
)

// defaultRemote is the git remote env branches live on.
const defaultRemote = "origin"

// hotfixPRLabel is the label that identifies an in-flight hotfix resolution PR.
const hotfixPRLabel = "cascade-hotfix"

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

// gitRunner abstracts the few git operations the planner performs so tests can
// observe and so dry-run can suppress mutation. The default implementation
// shells out to git in the current working directory.
type gitRunner interface {
	// ResolveSHA resolves a ref or short SHA to a full commit SHA.
	ResolveSHA(ref string) (string, error)
	// LocalBranchExists reports whether a local branch exists.
	LocalBranchExists(name string) (bool, error)
	// LocalBranchSHA returns the tip SHA of a local branch.
	LocalBranchSHA(name string) (string, error)
	// CreateBranch creates a branch pointing at sha.
	CreateBranch(name, sha string) error
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

func (execGitRunner) CreateBranch(name, sha string) error {
	if out, err := exec.Command("git", "branch", name, sha).CombinedOutput(); err != nil {
		return fmt.Errorf("git branch %s %s: %w\n%s", name, sha, err, out)
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
	gitRunner gitRunner
}

// PlannerOptions carries the required inputs for NewPlanner.
type PlannerOptions struct {
	ConfigPath  string
	ManifestKey string
	Actor       string
}

// Option configures optional, additive Planner behavior.
type Option func(*Planner)

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

// NewPlanner constructs a Planner from the manifest at opts.ConfigPath.
func NewPlanner(opts PlannerOptions, options ...Option) (*Planner, error) {
	key := opts.ManifestKey
	if key == "" {
		key = config.DefaultManifestKey
	}

	cicd, err := config.ParseManifestFile(opts.ConfigPath, key)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
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
	}
	for _, o := range options {
		o(p)
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
// target-env eligibility, no-op detection, env-branch reconciliation, and the
// single-flight open-PR gate.
func (p *Planner) Plan(fixRef, targetEnv string) (*PlanResult, error) {
	cfg := p.cicd.Config
	if cfg == nil {
		return nil, fmt.Errorf("manifest has no config block")
	}

	// Resolve the fix to a full SHA up front.
	fixSHA, err := p.gitRunner.ResolveSHA(fixRef)
	if err != nil {
		return nil, fmt.Errorf("resolving fix commit %q: %w", fixRef, err)
	}

	// 1. Trunk-ancestry gate: the fix must be an ancestor of trunk tip.
	trunkSHA, err := p.gitRunner.ResolveSHA("HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolving trunk tip: %w", err)
	}
	onTrunk, err := git.IsAncestor(fixSHA, trunkSHA)
	if err != nil {
		return nil, fmt.Errorf("checking trunk ancestry: %w", err)
	}
	if !onTrunk {
		return nil, fmt.Errorf("commit %s is not on trunk: a hotfix must apply a commit that is already an ancestor of trunk; merge it to trunk first", short(fixSHA))
	}

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

	branch := envBranch(targetEnv)
	result := &PlanResult{
		TargetEnv:             targetEnv,
		FixSHA:                fixSHA,
		Branch:                branch,
		BaseSHA:               baseSHA,
		ProtectionSuggestions: protectionSuggestions(branch),
		DryRun:                p.dryRun,
	}

	// 3. No-op check: fix already contained in the target state SHA.
	already, err := git.IsAncestor(fixSHA, baseSHA)
	if err != nil {
		return nil, fmt.Errorf("checking whether fix is already in %q: %w", targetEnv, err)
	}
	if already {
		result.NoOp = true
		return result, nil
	}

	// Compute the hotfix version candidate from the env's current version.
	candidate, err := hotfixVersionCandidate(state.Version)
	if err != nil {
		return nil, err
	}
	result.HotfixVersionCandidate = candidate

	// 4. env/<target> branch reconciliation.
	created, err := p.reconcileBranch(branch, baseSHA)
	if err != nil {
		return nil, err
	}
	result.BranchCreated = created

	// 5. Single-flight: refuse if a hotfix PR already targets env/<target>.
	openPRs, err := p.prChecker.OpenHotfixPRs(branch)
	if err != nil {
		return nil, fmt.Errorf("checking for open hotfix PRs: %w", err)
	}
	if len(openPRs) > 0 {
		pr := openPRs[0]
		return nil, fmt.Errorf("a hotfix PR (#%d %s) labeled %q already targets %s; resolve and finalize it, then re-dispatch this hotfix",
			pr.Number, pr.URL, hotfixPRLabel, branch)
	}

	return result, nil
}

// reconcileBranch ensures env/<target> exists at baseSHA. If absent it is
// created at baseSHA (unless dry-run, where creation is only reported). If
// present its tip must equal baseSHA, otherwise the run is aborted with replay
// guidance. Returns whether the branch was (or would be) created.
func (p *Planner) reconcileBranch(branch, baseSHA string) (bool, error) {
	exists, err := p.gitRunner.LocalBranchExists(branch)
	if err != nil {
		return false, fmt.Errorf("checking branch %s: %w", branch, err)
	}

	if exists {
		tip, err := p.gitRunner.LocalBranchSHA(branch)
		if err != nil {
			return false, fmt.Errorf("reading tip of %s: %w", branch, err)
		}
		if tip != baseSHA {
			return false, fmt.Errorf(
				"branch %s tip %s does not match recorded state SHA %s; this indicates an interrupted hotfix or manual edits: replay the hotfix workflow for the open PR, or reset %s to %s, before re-running",
				branch, short(tip), short(baseSHA), branch, short(baseSHA))
		}
		return false, nil
	}

	// Branch absent: it will be created at baseSHA.
	if p.dryRun {
		return true, nil
	}
	if err := p.gitRunner.CreateBranch(branch, baseSHA); err != nil {
		return false, fmt.Errorf("creating %s at %s: %w", branch, short(baseSHA), err)
	}
	return true, nil
}

// hotfixVersionCandidate returns the next free hotfix version over the base of
// envVersion. An rc version yields its first nested hotfix segment.
func hotfixVersionCandidate(envVersion string) (string, error) {
	if envVersion == "" {
		return "", fmt.Errorf("target environment has no recorded version; cannot compute hotfix version")
	}
	v, err := version.Parse(envVersion)
	if err != nil {
		return "", fmt.Errorf("parsing target version %q: %w", envVersion, err)
	}
	return v.NextHotfix().String(), nil
}

// envBranch returns the integration branch name for an environment.
func envBranch(env string) string {
	return "env/" + env
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
	}
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
