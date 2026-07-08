package rollback

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/ghaoutput"
	"github.com/stablekernel/cascade/internal/statewrite"
)

// newPreflightCommand creates the `cascade rollback preflight` subcommand. It
// resolves the rollback target read-only and, with --gha-output, writes the
// resolved environment, SHA, and version to $GITHUB_OUTPUT for the deploy and
// finalize jobs to consume. It never mutates state.
func newPreflightCommand() *cobra.Command {
	var (
		configPath  string
		manifestKey string
		env         string
		to          string
		deployable  string
		component   string
		ghaOutput   bool
		jsonOutput  bool
	)

	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Resolve a rollback target without modifying state",
		Long: `Resolve the rollback target for an environment and report it.

This is the read-only first stage of the generated rollback workflow. It
resolves the target SHA/version (from live state, the deploy-history ring, or
manifest history) and, with --gha-output, writes target_env, target_sha,
target_version, and can_proceed to $GITHUB_OUTPUT. It writes no manifest state.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPreflight(preflightOptions{
				configPath:  configPath,
				manifestKey: manifestKey,
				env:         env,
				to:          to,
				deployable:  deployable,
				component:   component,
				ghaOutput:   ghaOutput,
				jsonOutput:  jsonOutput,
			})
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to manifest file (default: .github/manifest.yaml)")
	cmd.Flags().StringVar(&manifestKey, "key", config.DefaultManifestKey, "Top-level manifest key")
	cmd.Flags().StringVar(&env, "env", "", "Target environment to roll back (required)")
	cmd.Flags().StringVar(&to, "to", "", "Prior version or SHA to re-promote (optional; defaults to the previous version)")
	cmd.Flags().StringVar(&deployable, "deployable", "", "Scope the rollback to a single deployable")
	cmd.Flags().StringVar(&component, "component", "", "Scope the rollback to a declared component")
	cmd.Flags().BoolVar(&ghaOutput, "gha-output", false, "Write resolved target to $GITHUB_OUTPUT")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print the resolved plan as JSON")

	_ = cmd.MarkFlagRequired("env")

	return cmd
}

type preflightOptions struct {
	configPath  string
	manifestKey string
	env         string
	to          string
	deployable  string
	component   string
	ghaOutput   bool
	jsonOutput  bool
}

func runPreflight(opts preflightOptions) error {
	rb, err := New(Options{
		ConfigPath:  opts.configPath,
		ManifestKey: opts.manifestKey,
		Component:   opts.component,
	})
	if err != nil {
		if opts.ghaOutput {
			writeCannotProceed()
		}
		return err
	}

	plan, err := rb.Plan(opts.env, opts.to, opts.deployable)
	if err != nil {
		if opts.ghaOutput {
			writeCannotProceed()
		}
		return err
	}

	if opts.ghaOutput {
		w := ghaoutput.New()
		w.Set("target_env", plan.Environment)
		w.Set("target_sha", plan.Target.SHA)
		w.Set("target_version", plan.Target.Version)
		w.Set("target_source", plan.Target.Source)
		w.SetBool("can_proceed", true)
		return w.Flush()
	}

	if opts.jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(plan)
	}

	fmt.Printf("environment %s would roll back to %s (%s) [resolved from %s]\n",
		plan.Environment, orDash(plan.Target.Version), truncate(plan.Target.SHA), plan.Target.Source)
	return nil
}

// writeCannotProceed emits can_proceed=false so the workflow's fail step trips
// when resolution fails. A best-effort flush: the returned resolution error is
// the authoritative signal, so a flush failure here is intentionally ignored.
func writeCannotProceed() {
	w := ghaoutput.New()
	w.SetBool("can_proceed", false)
	_ = w.Flush()
}

// newFinalizeCommand creates the `cascade rollback finalize` subcommand. It
// resolves the target, applies the rollback to state (marking the environment
// diverged), and, with --commit-push, writes the manifest back to the trunk
// branch using the same state-write mechanism as a promotion finalize.
func newFinalizeCommand() *cobra.Command {
	var (
		configPath  string
		manifestKey string
		env         string
		to          string
		deployable  string
		component   string
		actor       string
		commitPush  bool
	)

	cmd := &cobra.Command{
		Use:   "finalize",
		Short: "Apply a rollback and persist state to trunk",
		Long: `Apply a resolved rollback and persist the updated manifest.

This is the final stage of the generated rollback workflow. It resolves the
target, applies it to state (marking the environment diverged so forward-
promotion guards treat it as off-trunk until a promotion rejoins it), and, with
--commit-push, commits the manifest back to the trunk branch.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFinalize(finalizeOptions{
				configPath:  configPath,
				manifestKey: manifestKey,
				env:         env,
				to:          to,
				deployable:  deployable,
				component:   component,
				actor:       actor,
				commitPush:  commitPush,
			})
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to manifest file (default: .github/manifest.yaml)")
	cmd.Flags().StringVar(&manifestKey, "key", config.DefaultManifestKey, "Top-level manifest key")
	cmd.Flags().StringVar(&env, "env", "", "Target environment to roll back (required)")
	cmd.Flags().StringVar(&to, "to", "", "Prior version or SHA to re-promote (optional; defaults to the previous version)")
	cmd.Flags().StringVar(&deployable, "deployable", "", "Scope the rollback to a single deployable")
	cmd.Flags().StringVar(&component, "component", "", "Scope the rollback to a declared component")
	cmd.Flags().StringVar(&actor, "actor", "", "Actor recorded on the rollback (default: $GITHUB_ACTOR)")
	cmd.Flags().BoolVar(&commitPush, "commit-push", false, "Commit and push the updated manifest to the trunk branch")

	_ = cmd.MarkFlagRequired("env")

	return cmd
}

type finalizeOptions struct {
	configPath  string
	manifestKey string
	env         string
	to          string
	deployable  string
	component   string
	actor       string
	commitPush  bool
}

func runFinalize(opts finalizeOptions) error {
	rb, err := New(Options{
		ConfigPath:  opts.configPath,
		ManifestKey: opts.manifestKey,
		Actor:       opts.actor,
		Component:   opts.component,
	})
	if err != nil {
		return err
	}

	plan, err := rb.Plan(opts.env, opts.to, opts.deployable)
	if err != nil {
		return err
	}

	// Gate the state write on the actual deploy results. A rollback re-deploys a
	// prior SHA; finalize must not record the environment as rolled back unless
	// that deploy actually succeeded. Otherwise a failed deploy would still mark
	// the environment diverged at the prior SHA, asserting a rollback that never
	// landed. See gateOnDeployResults for the in-scope rules.
	if err := gateOnDeployResults(rb.DeployNames(), opts.deployable); err != nil {
		return err
	}

	if err := rb.Apply(plan); err != nil {
		return fmt.Errorf("applying rollback: %w", err)
	}

	if opts.commitPush {
		if err := rb.CommitAndPush(); err != nil {
			return fmt.Errorf("failed to commit and push: %w", err)
		}
		fmt.Printf("State updated and committed for %s\n", plan.Environment)
		return nil
	}

	fmt.Printf("State updated for %s (not committed)\n", plan.Environment)
	return nil
}

// gateOnDeployResults decides whether the rollback state write may proceed,
// based on the reported result of each configured deploy job. The generated
// finalize job runs with always() so it observes every deploy result, including
// failures; this is where that observation becomes a gate.
//
// Rules:
//   - No deploys configured: a state-only, deploy-less rollback. There is no
//     deploy to gate on, so it always proceeds (returns nil).
//   - In scope: when deployable is set, only that deploy is in scope; otherwise
//     every configured deploy is in scope. A deploy excluded by the --deployable
//     filter reports "skipped", which is never treated as a failure.
//   - Any in-scope deploy reporting "failure" or "cancelled" aborts the write
//     with an error naming the deploy, leaving trunk state unchanged.
//   - If deploys are in scope but none of them succeeded (all skipped or
//     unreported), nothing was actually deployed, so the write is also aborted.
//   - Otherwise (at least one in-scope deploy succeeded and none failed) the
//     write proceeds.
func gateOnDeployResults(deployNames []string, deployable string) error {
	if len(deployNames) == 0 {
		return nil
	}

	results := readDeployResultsFromEnv(deployNames)

	anySucceeded := false
	for _, name := range deployNames {
		if deployable != "" && name != deployable {
			continue // Out of scope: excluded by the --deployable filter.
		}
		result := results[name]
		switch result {
		case "failure", "cancelled":
			return fmt.Errorf("rollback aborted: deploy %q did not succeed (result=%s); environment state left unchanged", name, result)
		case "success":
			anySucceeded = true
		}
	}

	if !anySucceeded {
		return fmt.Errorf("rollback aborted: no in-scope deploy succeeded; environment state left unchanged")
	}
	return nil
}

// readDeployResultsFromEnv reads DEPLOY_RESULT_<NAME> env vars and returns a map
// of deploy name to its reported conclusion for each deploy with a non-empty
// value. Hyphens in deploy names become underscores and the name is uppercased,
// matching the keys the generated finalize job writes.
func readDeployResultsFromEnv(deployNames []string) map[string]string {
	results := make(map[string]string, len(deployNames))
	for _, name := range deployNames {
		key := "DEPLOY_RESULT_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		if val := os.Getenv(key); val != "" {
			results[name] = val
		}
	}
	return results
}

// commitAndPush persists the manifest at path back to the trunk branch.
//
// On real GitHub the write goes through the Contents REST API (via the gh CLI):
// API-created commits are signed by GitHub and, with a bypass-capable token, can
// update a protected trunk. In the act/gitea environment there is no GitHub API,
// so the change is committed and pushed with plain git. The environment is
// detected exactly as the promote finalize path does, by GITHUB_SERVER_URL.
func commitAndPush(path, env string, author statewrite.Identity) error {
	status, err := exec.Command("git", "status", "--porcelain", path).Output()
	if err != nil {
		return fmt.Errorf("git status failed: %w", err)
	}
	if len(status) == 0 {
		return nil // No changes
	}

	message := fmt.Sprintf("chore: update state after rollback of %s [skip ci]", env)

	if isRealGitHub() {
		return writeStateViaAPI(path, message, author)
	}
	return commitAndPushGit(path, message, author)
}

// isRealGitHub reports whether the workflow runs on github.com rather than an
// act/gitea environment, mirroring the promote finalize detection.
func isRealGitHub() bool {
	server := os.Getenv("GITHUB_SERVER_URL")
	return server == "" || server == "https://github.com"
}

// writeStateViaAPI writes the manifest to the trunk branch through the GitHub
// Contents REST API using the gh CLI, producing a signed commit that can update
// a protected branch when the token is bypass-capable.
func writeStateViaAPI(path, message string, author statewrite.Identity) error {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return fmt.Errorf("GITHUB_REPOSITORY is not set; cannot write state via API")
	}
	branch := trunkBranchFromEnv()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read manifest failed: %w", err)
	}
	contentB64 := base64.StdEncoding.EncodeToString(data)

	apiPath := fmt.Sprintf("repos/%s/contents/%s", repo, path)

	shaOut, _ := exec.Command("gh", "api", fmt.Sprintf("%s?ref=%s", apiPath, branch), "--jq", ".sha").Output()
	currentSHA := strings.TrimSpace(string(shaOut))

	args := buildStatePutArgs(apiPath, branch, currentSHA, message, contentB64, author)

	if out, err := exec.Command("gh", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("state write via API failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// buildStatePutArgs assembles the gh CLI arguments for the Contents API PUT that
// writes the manifest. It stamps both author and committer with the resolved
// identity so the API attributes the commit to the bot rather than the token
// owner GitHub would otherwise use when those fields are absent. An empty
// currentSHA creates the file rather than guarding an update.
func buildStatePutArgs(apiPath, branch, currentSHA, message, contentB64 string, author statewrite.Identity) []string {
	id := author.OrDefault()
	args := []string{
		"api", apiPath, "-X", "PUT",
		"-f", "message=" + message,
		"-f", "content=" + contentB64,
		"-f", "branch=" + branch,
		"-f", "author[name]=" + id.Name,
		"-f", "author[email]=" + id.Email,
		"-f", "committer[name]=" + id.Name,
		"-f", "committer[email]=" + id.Email,
	}
	if currentSHA != "" {
		args = append(args, "-f", "sha="+currentSHA)
	}
	return args
}

// commitAndPushGit commits the manifest and pushes with plain git, used in the
// act/gitea environment which enforces neither branch protection nor signatures.
func commitAndPushGit(path, message string, author statewrite.Identity) error {
	id := author.OrDefault()
	if err := exec.Command("git", "config", "user.name", id.Name).Run(); err != nil {
		return fmt.Errorf("git config user.name failed: %w", err)
	}
	if err := exec.Command("git", "config", "user.email", id.Email).Run(); err != nil {
		return fmt.Errorf("git config user.email failed: %w", err)
	}
	if err := exec.Command("git", "add", path).Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}
	if err := exec.Command("git", "commit", "-m", message).Run(); err != nil {
		return fmt.Errorf("git commit failed: %w", err)
	}

	// Push HEAD to the trunk branch explicitly: a workflow_dispatch run checks out
	// a detached HEAD, so a bare push has no upstream. HEAD:refs/heads/<trunk>
	// works regardless of detached state and targets the branch state belongs on.
	branch := trunkBranchFromEnv()
	if out, err := exec.Command("git", "push", "origin", "HEAD:refs/heads/"+branch).CombinedOutput(); err != nil {
		return fmt.Errorf("git push failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// trunkBranchFromEnv resolves the branch to write state to, taking it from
// GITHUB_REF when present and falling back to "main".
func trunkBranchFromEnv() string {
	ref := os.Getenv("GITHUB_REF")
	if strings.HasPrefix(ref, "refs/heads/") {
		return strings.TrimPrefix(ref, "refs/heads/")
	}
	if ref != "" {
		return ref
	}
	return "main"
}
