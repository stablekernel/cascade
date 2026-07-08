package hotfix

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/ghaoutput"
)

// NewCommand creates the `cascade hotfix` command and its subcommands.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hotfix",
		Short: "Apply a trunk commit onto an environment pinned to an older base",
		Long: `Manage per-environment hotfixes.

A hotfix applies a single trunk commit onto an environment whose state is pinned
to an older trunk base, without dragging in the intervening commits. The fix must
already be on trunk; cascade refuses to apply a commit that is not an ancestor of
trunk tip.

Subcommands compute and validate the hotfix; the cherry-pick, build, deploy, and
state write run in the generated workflow.`,
	}

	cmd.AddCommand(newPlanCommand())
	cmd.AddCommand(newFinalizeCommand())
	return cmd
}

// newFinalizeCommand creates the `cascade hotfix finalize` subcommand.
func newFinalizeCommand() *cobra.Command {
	var (
		configPath  string
		manifestKey string
		targetEnv   string
		mergeSHA    string
		fixSHA      string
		baseSHA     string
		actor       string
		component   string
		dryRun      bool
		deployFlags []string
		buildFlags  []string
	)

	cmd := &cobra.Command{
		Use:   "finalize",
		Short: "Write diverged state, tag, and release for a merged hotfix",
		Long: `Finalize a completed hotfix.

After the resolution PR merges and the build and deploy succeed, this command:
  1. Cross-checks the merge SHA equals the env/<target> branch tip
  2. Allocates the next free hotfix version over the env's current version
  3. Snapshots the prior env state into the rollback ring
  4. Writes the diverged state (sha, version, ref, base_sha, patches) and substates
  5. Commits the manifest to trunk with the rebase-retry push
  6. Creates the hotfix tag and release object

The verb is idempotent on identical inputs: a rerun after the state already
records the merge SHA is a no-op.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := []FinalizeOption{WithFinalizeDryRun(dryRun), WithComponent(component)}

			finalizer, err := NewFinalizer(FinalizerOptions{
				ConfigPath:  configPath,
				ManifestKey: manifestKey,
				Actor:       actor,
			}, opts...)
			if err != nil {
				return err
			}

			for _, df := range deployFlags {
				name, result, ok := splitResultFlag(df)
				if !ok {
					return fmt.Errorf("invalid --deploy-result %q: want name=result", df)
				}
				finalizer.SetDeployResult(name, result)
			}
			for _, bf := range buildFlags {
				name, result, ok := splitResultFlag(bf)
				if !ok {
					return fmt.Errorf("invalid --build-result %q: want name=result", bf)
				}
				finalizer.SetBuildResult(name, result)
			}

			fixSHAs, err := parseCommitRefs(fixSHA)
			if err != nil {
				return fmt.Errorf("invalid --fix-sha %q: %w", fixSHA, err)
			}

			return finalizer.Finalize(targetEnv, mergeSHA, fixSHAs, baseSHA)
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to manifest file (default: .github/manifest.yaml)")
	cmd.Flags().StringVar(&manifestKey, "key", config.DefaultManifestKey, "Top-level manifest key")
	cmd.Flags().StringVar(&targetEnv, "target-env", "", "Environment to finalize (required)")
	cmd.Flags().StringVar(&mergeSHA, "merge-sha", "", "Tip of env/<target> after the resolution PR merged (required)")
	cmd.Flags().StringVar(&fixSHA, "fix-sha", "", "Trunk commit(s) the hotfix carries; comma-delimited for a multi-commit set (required)")
	cmd.Flags().StringVar(&baseSHA, "base-sha", "", "Trunk anchor the integration branch diverged from (required)")
	cmd.Flags().StringVar(&actor, "actor", "", "Actor recorded on the state (default: $GITHUB_ACTOR)")
	cmd.Flags().StringVar(&component, "component", "", "Declared component to scope the hotfix to (default: single-component manifest)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate and compute without writing state, tags, or releases")
	cmd.Flags().StringArrayVar(&deployFlags, "deploy-result", nil, "Deploy result as name=result (repeatable)")
	cmd.Flags().StringArrayVar(&buildFlags, "build-result", nil, "Build result as name=result (repeatable)")

	_ = cmd.MarkFlagRequired("target-env")
	_ = cmd.MarkFlagRequired("merge-sha")
	_ = cmd.MarkFlagRequired("fix-sha")
	_ = cmd.MarkFlagRequired("base-sha")

	return cmd
}

// splitResultFlag parses a "name=result" job-result flag value.
func splitResultFlag(s string) (name, result string, ok bool) {
	idx := strings.IndexByte(s, '=')
	if idx <= 0 || idx == len(s)-1 {
		return "", "", false
	}
	return s[:idx], s[idx+1:], true
}

// newPlanCommand creates the `cascade hotfix plan` subcommand.
func newPlanCommand() *cobra.Command {
	var (
		configPath  string
		manifestKey string
		commitRef   string
		commitsRef  string
		targetEnv   string
		actor       string
		remote      string
		repo        string
		component   string
		dryRun      bool
		jsonOutput  bool
		ghaOutput   bool
	)

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Validate and plan a hotfix to an environment",
		Long: `Validate and plan a hotfix.

This command:
  1. Verifies the fix commit is an ancestor of trunk tip (trunk-first gate)
  2. Checks the target is a configured env and not the first env (prod is allowed)
  3. Reports a no-op when the fix is already contained in the target
  4. Reconciles the env/<target> integration branch at the recorded state SHA
  5. Refuses to proceed when a cascade-hotfix PR already targets env/<target>

It computes the env branch, base SHA, hotfix version candidate, and ready-to-run
branch-protection command suggestions. The cherry-pick, build, deploy, and state
write happen in the generated workflow; this verb computes and validates only.

With --dry-run nothing is mutated (the env branch is planned but not created).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := []Option{
				WithDryRun(dryRun),
				WithRemote(remote),
				WithPlanComponent(component),
			}
			if repo != "" {
				opts = append(opts, WithPRChecker(newRestPRChecker(repo)))
			}

			planner, err := NewPlanner(PlannerOptions{
				ConfigPath:  configPath,
				ManifestKey: manifestKey,
				Actor:       actor,
			}, opts...)
			if err != nil {
				return err
			}

			// --commits selects the multi-commit, multi-env chain path. It is
			// additive: --commit remains the single-commit, single-env verb.
			if commitsRef != "" {
				refs, err := parseCommitRefs(commitsRef)
				if err != nil {
					return err
				}
				chain, err := planner.PlanChain(refs, targetEnv)
				if err != nil {
					return err
				}
				switch {
				case ghaOutput:
					return writePlanChainGHAOutput(chain)
				case jsonOutput:
					return outputJSON(chain)
				default:
					printPlanChain(chain)
					return nil
				}
			}

			result, err := planner.Plan(commitRef, targetEnv)
			if err != nil {
				return err
			}

			switch {
			case ghaOutput:
				return writePlanGHAOutput(result)
			case jsonOutput:
				return outputJSON(result)
			default:
				printPlan(result)
				return nil
			}
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to manifest file (default: .github/manifest.yaml)")
	cmd.Flags().StringVar(&manifestKey, "key", config.DefaultManifestKey, "Top-level manifest key")
	cmd.Flags().StringVar(&commitRef, "commit", "", "Trunk commit (SHA or ref) carrying the fix (single-env path)")
	cmd.Flags().StringVar(&commitsRef, "commits", "", "Comma-delimited trunk commits to elevate across the env chain up to --target-env")
	cmd.Flags().StringVar(&targetEnv, "target-env", "", "Environment to hotfix (required)")
	cmd.Flags().StringVar(&actor, "actor", "", "Actor recorded on the plan (default: $GITHUB_ACTOR)")
	cmd.Flags().StringVar(&remote, "remote", defaultRemote, "Git remote env branches live on")
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo for single-flight PR lookup via the REST API (default: skip the check)")
	cmd.Flags().StringVar(&component, "component", "", "Declared component to scope the hotfix to (default: single-component manifest)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Compute the plan without mutating anything")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output the plan as JSON")
	cmd.Flags().BoolVar(&ghaOutput, "gha-output", false, "Write outputs to $GITHUB_OUTPUT for workflow consumption")

	cmd.MarkFlagsMutuallyExclusive("commit", "commits")
	cmd.MarkFlagsOneRequired("commit", "commits")
	_ = cmd.MarkFlagRequired("target-env")

	return cmd
}

// restPRChecker implements PRChecker via the GitHub/Gitea REST API. It calls
// GET {GITHUB_API_URL}/repos/{owner}/{repo}/pulls?state=open (paginated) and
// filters client-side by base branch and the cascade-hotfix label. This avoids
// depending on the gh CLI, which is not present in the pinned act runner image
// used by the e2e harness, and works identically against both github.com and a
// gitea server (both expose the same endpoint and response fields).
type restPRChecker struct {
	repo string
	// apiBase and token override the corresponding environment variables when
	// non-empty, so tests can inject an httptest server URL without setenv.
	apiBase string
	token   string
}

// newRestPRChecker creates a PRChecker that queries the REST API for owner/repo.
// The API base URL is read from GITHUB_API_URL at call time; the authentication
// token is read from GH_TOKEN (then GITHUB_TOKEN) at call time. Both variables
// are populated automatically in GitHub Actions and by the act e2e harness.
func newRestPRChecker(repo string) *restPRChecker {
	return &restPRChecker{repo: repo}
}

// resolvedAPIBase returns the effective REST API base URL.
func (c *restPRChecker) resolvedAPIBase() string {
	if c.apiBase != "" {
		return c.apiBase
	}
	if u := os.Getenv("GITHUB_API_URL"); u != "" {
		return u
	}
	return "https://api.github.com"
}

// resolvedToken returns the effective authentication token.
func (c *restPRChecker) resolvedToken() string {
	if c.token != "" {
		return c.token
	}
	if t := os.Getenv("GH_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("GITHUB_TOKEN")
}

// prListEntry is the subset of a pull-request REST response the checker needs.
type prListEntry struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Base    struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// OpenHotfixPRs lists open PRs labeled cascade-hotfix whose base is
// baseBranch. It queries the REST API with pagination (following Link rel=next)
// and filters client-side so the result is correct regardless of which
// server-side filter parameters a given gitea version supports.
func (c *restPRChecker) OpenHotfixPRs(baseBranch string) ([]OpenPR, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	token := c.resolvedToken()
	url := c.resolvedAPIBase() + "/repos/" + c.repo + "/pulls?state=open&per_page=100"

	var result []OpenPR
	for url != "" {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("building PR list request: %w", err)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		if token != "" {
			if i := strings.IndexByte(token, ':'); i >= 0 {
				// "username:password" basic-auth credential (used by the act e2e
				// harness, which passes the gitea admin credential as GITHUB_TOKEN).
				req.SetBasicAuth(token[:i], token[i+1:])
			} else {
				req.Header.Set("Authorization", "token "+token)
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("PR list request to %s: %w", url, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("reading PR list response: %w", readErr)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("PR list returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var page []prListEntry
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("parsing PR list response: %w", err)
		}

		for _, pr := range page {
			if pr.Base.Ref != baseBranch {
				continue
			}
			for _, l := range pr.Labels {
				// Both labels indicate an in-flight hotfix that must block the gate:
				// cascade-hotfix is applied to a clean cherry-pick resolution PR;
				// cascade-hotfix-conflict is applied when the cherry-pick conflicted
				// and a human is actively resolving it. Either label means real work
				// is in progress on env/<branch>; resetting would destroy that work.
				if l.Name == hotfixPRLabel || l.Name == hotfixConflictPRLabel {
					result = append(result, OpenPR{Number: pr.Number, URL: pr.HTMLURL})
					break
				}
			}
		}

		url = nextPageURL(resp.Header.Get("Link"))
	}
	return result, nil
}

// nextPageURL extracts the rel="next" URL from a GitHub/Gitea Link header.
// It returns the empty string when no next page exists.
func nextPageURL(link string) string {
	if link == "" {
		return ""
	}
	for _, part := range strings.Split(link, ",") {
		part = strings.TrimSpace(part)
		semi := strings.IndexByte(part, ';')
		if semi < 0 {
			continue
		}
		rawURL := strings.TrimSpace(part[:semi])
		rel := strings.TrimSpace(part[semi+1:])
		if !strings.HasPrefix(rawURL, "<") || !strings.HasSuffix(rawURL, ">") {
			continue
		}
		if rel == `rel="next"` {
			return rawURL[1 : len(rawURL)-1]
		}
	}
	return ""
}

func writePlanGHAOutput(result *PlanResult) error {
	w := ghaoutput.New()
	w.Set("target_env", result.TargetEnv)
	w.Set("fix_sha", result.FixSHA)
	w.Set("branch", result.Branch)
	w.Set("base_sha", result.BaseSHA)
	w.SetBool("no_op", result.NoOp)
	w.SetBool("branch_created", result.BranchCreated)
	w.SetBool("branch_reset", result.BranchReset)
	w.Set("hotfix_version_candidate", result.HotfixVersionCandidate)
	w.SetBool("conflict_expected", result.ConflictExpected)
	w.SetBool("dry_run", result.DryRun)
	if err := w.SetJSON("protection_suggestions", result.ProtectionSuggestions); err != nil {
		return err
	}
	w.SetMultiline("protection_suggestions_text", strings.Join(result.ProtectionSuggestions, "\n"))
	if err := w.Flush(); err != nil {
		return err
	}
	// Emit a diagnostic line when the orphan self-heal fires so the event is
	// observable in workflow logs without requiring a downstream step to echo
	// the branch_reset output. The line also serves as the e2e assertion target.
	if result.BranchReset {
		fmt.Fprintf(os.Stderr, "cascade: orphan %s branch was reset to recorded base %s\n",
			result.Branch, short(result.BaseSHA))
	}
	return nil
}

// chainGHAOutputs renders a PlanChainResult into a deterministic, additive set
// of GHA output keys describing the bottom-up env sequence and the per-env
// commit lists. Keys are independent of the single-env writePlanGHAOutput keys
// so both can be emitted from one plan run without collision.
//
//   - env_sequence: comma-joined bottom-up env list
//   - env_count: number of envs in the chain
//   - commits_<env>: comma-joined fix SHAs still to apply for that env
//   - no_op_<env>: whether that env's whole requested set is already present
//   - conflict_expected_<env>: best-effort cherry-pick conflict hint
//   - reset_<env>: whether the orphan self-heal reset that env branch to its base
func chainGHAOutputs(result *PlanChainResult) (simple map[string]string, multiline map[string]string) {
	simple = make(map[string]string)
	multiline = make(map[string]string)

	envNames := make([]string, 0, len(result.Envs))
	for _, ep := range result.Envs {
		envNames = append(envNames, ep.Env)
		simple["commits_"+ep.Env] = strings.Join(ep.Commits, ",")
		simple["no_op_"+ep.Env] = fmt.Sprintf("%v", ep.NoOp)
		simple["conflict_expected_"+ep.Env] = fmt.Sprintf("%v", ep.ConflictExpected)
		simple["reset_"+ep.Env] = fmt.Sprintf("%v", ep.BranchReset)
		simple["base_"+ep.Env] = ep.BaseSHA
	}
	simple["env_sequence"] = strings.Join(envNames, ",")
	simple["env_count"] = fmt.Sprintf("%d", len(envNames))
	return simple, multiline
}

// writePlanChainGHAOutput emits the chain plan as additive GHA outputs. It does
// not remove or overwrite the single-env writePlanGHAOutput keys.
func writePlanChainGHAOutput(result *PlanChainResult) error {
	w := ghaoutput.New()
	simple, multiline := chainGHAOutputs(result)
	for k, v := range simple {
		w.Set(k, v)
	}
	for k, v := range multiline {
		w.SetMultiline(k, v)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	// Emit a diagnostic line for each env whose orphan branch was self-healed so
	// the event is observable in workflow logs (mirrors writePlanGHAOutput).
	for _, ep := range result.Envs {
		if ep.BranchReset {
			fmt.Fprintf(os.Stderr, "cascade: orphan %s branch was reset to recorded base %s\n",
				ep.Branch, short(ep.BaseSHA))
		}
	}
	return nil
}

// printPlanChain renders the human-readable chain plan: the bottom-up env
// sequence and, per env, the commits still to apply (or a no-op marker).
func printPlanChain(result *PlanChainResult) {
	fmt.Printf("Env chain:   %d environment(s), bottom-up\n", len(result.Envs))
	for _, ep := range result.Envs {
		if ep.NoOp {
			fmt.Printf("  %-10s no-op (all requested commits already present)\n", ep.Env)
			continue
		}
		shorts := make([]string, 0, len(ep.Commits))
		for _, c := range ep.Commits {
			shorts = append(shorts, short(c))
		}
		fmt.Printf("  %-10s %s (base %s): apply %s\n",
			ep.Env, ep.Branch, short(ep.BaseSHA), strings.Join(shorts, ", "))
	}
}

func outputJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func printPlan(result *PlanResult) {
	fmt.Printf("Target env:  %s\n", result.TargetEnv)
	fmt.Printf("Fix commit:  %s\n", short(result.FixSHA))
	if result.NoOp {
		fmt.Printf("Result:      no-op (fix is already in %s)\n", result.TargetEnv)
		return
	}
	fmt.Printf("Env branch:  %s (base %s)\n", result.Branch, short(result.BaseSHA))
	if result.BranchCreated {
		if result.DryRun {
			fmt.Printf("             would create %s at %s\n", result.Branch, short(result.BaseSHA))
		} else {
			fmt.Printf("             created %s at %s\n", result.Branch, short(result.BaseSHA))
		}
	} else {
		fmt.Printf("             %s already present at the recorded SHA\n", result.Branch)
	}
	fmt.Printf("Version:     %s\n", result.HotfixVersionCandidate)
	if result.DryRun {
		fmt.Println("Mode:        dry-run (no mutations)")
	}

	fmt.Println()
	fmt.Println("Suggested env/* branch protection (cascade does not apply these):")
	for _, s := range result.ProtectionSuggestions {
		fmt.Printf("  %s\n", s)
	}
}
