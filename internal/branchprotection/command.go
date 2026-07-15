package branchprotection

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/globals"
)

// Options configures a branch-protection emit run.
type Options struct {
	// ConfigPath is the manifest path; empty means auto-detect.
	ConfigPath string
	// ManifestKey is the key in the manifest file holding the CI config.
	ManifestKey string
	// Branch labels the protection target. It affects only the operator_todo
	// note; the PUT body itself is branch-agnostic. The required contexts (Setup
	// and Finalize) are the orchestrate-workflow steps jobs, identical across
	// branches and environments, so the branch never changes them.
	Branch string
	// Output is the destination path. Empty or "-" writes to stdout.
	Output string
	// Apply opts into calling GitHub: instead of emitting the JSON to stdout,
	// cascade PUTs the .protection body to the branches protection API. The emit
	// default (Apply false) is unchanged. When Output is also set, the JSON file
	// is still written for the operator's records before the apply runs.
	Apply bool
	// Token is the credential used for the apply. It should be a scoped PAT the
	// operator supplies that carries repo-admin; the workflow's GITHUB_TOKEN does
	// not need admin. Empty falls back to the GITHUB_TOKEN environment variable.
	Token string
	// Repo is the owner/repo the apply targets. Empty falls back to the
	// GITHUB_REPOSITORY environment variable.
	Repo string
	// APIURL is the REST API base for the apply. Empty falls back to GITHUB_API_URL
	// and then https://api.github.com. It exists so the apply is testable against a
	// mock server.
	APIURL string
}

// NewCommand creates the branch-protection command. It emits the JSON body an
// operator applies to GitHub's branch-protection API for a cascade-managed
// trunk. cascade emits the file; the operator applies it. cascade never calls
// the GitHub API.
func NewCommand() *cobra.Command {
	var o Options

	cmd := &cobra.Command{
		Use:   "branch-protection",
		Short: "Emit the branch-protection JSON body for an operator to apply",
		Long: `Emit the JSON body an operator applies to GitHub's branch-protection API for
a cascade-managed trunk. cascade emits the file; the operator applies it. cascade
never calls the GitHub API.

The output is a wrapper with two top-level keys:

  protection     the EXACT body to PUT to the branches protection API
  operator_todo  companion guidance that is NOT part of the PUT body

Apply it by sending only the .protection object, for example:

  cascade branch-protection | jq .protection | \
    gh api -X PUT repos/OWNER/REPO/branches/main/protection --input -

The required status checks contain only the cascade-controlled Setup and Finalize
jobs, which run on every pipeline run, so .protection applied as-is never blocks a
pull request. The reusable-workflow caller jobs (validate, build, deploy) are not
required directly because cascade knows their display-name prefix but not the
inner job name GitHub appends to form the real check-run context. Those prefixes
are listed under operator_todo.complete_these_contexts as "<DisplayName> /
<inner-job>" placeholders for you to complete.

The --branch flag only labels the guidance note; the required contexts are the
same across branches and environments because they are the orchestrate-workflow
steps jobs.

By default cascade only emits the JSON; it makes no API call. Pass --apply to PUT
the .protection body for you. Applying branch protection requires repo-admin, so
supply a scoped token with --token (or GITHUB_TOKEN) rather than relying on the
workflow's GITHUB_TOKEN. The --branch here is the real apply target, --repo is the
owner/repo (default GITHUB_REPOSITORY), and --api-url overrides the API base
(default GITHUB_API_URL, then https://api.github.com).`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return Run(o, cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringVarP(&o.ConfigPath, "config", "c", "", "Path to config file (default: auto-detect .github/manifest.yaml)")
	cmd.Flags().StringVar(&o.ManifestKey, "manifest-key", config.DefaultManifestKey, "Key in manifest file containing CI config")
	cmd.Flags().StringVar(&o.Branch, "branch", "main", "Branch the protection targets (with --apply this is the apply target; otherwise it only labels the guidance note)")
	cmd.Flags().StringVarP(&o.Output, "output", "o", "", "Write to this path instead of stdout ('-' also means stdout)")
	cmd.Flags().BoolVar(&o.Apply, "apply", false, "Apply the .protection body to GitHub instead of emitting JSON (requires --token with repo-admin)")
	cmd.Flags().StringVar(&o.Token, "token", "", "Token used for --apply (a scoped repo-admin PAT; default GITHUB_TOKEN)")
	cmd.Flags().StringVar(&o.Repo, "repo", "", "owner/repo the apply targets (default GITHUB_REPOSITORY)")
	cmd.Flags().StringVar(&o.APIURL, "api-url", "", "REST API base for --apply (default GITHUB_API_URL, then https://api.github.com)")

	return cmd
}

// Run resolves the manifest and builds the payload, then either emits it or
// applies it. By default (Options.Apply false) the behavior is unchanged: when
// Options.Output is empty or "-" it writes the JSON to stdout, otherwise to that
// file path, and cascade makes no API call. When Options.Apply is set it PUTs the
// .protection body to GitHub; a non-empty Output still writes the JSON file first
// for the operator's records.
func Run(o Options, stdout io.Writer) error {
	configPath := o.ConfigPath
	if configPath == "" {
		configPath = config.FindConfigFile("")
	}

	manifestKey := o.ManifestKey
	if manifestKey == "" {
		manifestKey = config.DefaultManifestKey
	}

	cfg, err := config.ParseWithKey(configPath, manifestKey)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	if errs := config.Validate(cfg); len(errs) > 0 {
		return fmt.Errorf("config validation failed: %s", errs[0])
	}

	branch := o.Branch
	if branch == "" {
		branch = "main"
	}

	payload := Build(cfg, branch)
	out, err := Marshal(payload)
	if err != nil {
		return err
	}

	// A real Output path is always honored so an operator can keep the emitted
	// JSON on disk whether or not they also apply it.
	if o.Output != "" && o.Output != "-" {
		if werr := os.WriteFile(o.Output, out, 0o644); werr != nil {
			return fmt.Errorf("writing branch-protection payload to %s: %w", o.Output, werr)
		}
	}

	if o.Apply {
		return runApply(o, branch, payload.Protection, stdout)
	}

	// Emit-to-stdout default: unchanged from the original command behavior.
	if o.Output == "" || o.Output == "-" {
		if _, werr := stdout.Write(out); werr != nil {
			return fmt.Errorf("writing branch-protection payload: %w", werr)
		}
	}
	return nil
}

// runApply resolves the apply inputs, PUTs the protection body to GitHub, and
// prints a one-line confirmation. A missing token or repository is a usage error
// surfaced before any network call. Under --dry-run the resolved PUT target is
// printed and no request is issued; a token is deliberately not required to
// preview.
func runApply(o Options, branch string, protection Protection, stdout io.Writer) error {
	repo := resolveRepo(o.Repo)
	if repo == "" {
		return fmt.Errorf("--apply requires a repository: pass --repo owner/repo or set GITHUB_REPOSITORY")
	}

	if globals.DryRun() {
		_, err := fmt.Fprintf(stdout, "Dry run - would apply branch protection to %s/branches/%s (PUT %s/repos/%s/branches/%s/protection)\n",
			repo, branch, resolveAPIURL(o.APIURL), repo, branch)
		if err != nil {
			return fmt.Errorf("writing dry-run preview: %w", err)
		}
		return nil
	}

	token := resolveToken(o.Token)
	if token == "" {
		return fmt.Errorf("--apply requires a token: pass --token or set GITHUB_TOKEN")
	}

	apiURL := resolveAPIURL(o.APIURL)

	if err := newApplier(apiURL, token).apply(context.Background(), repo, branch, protection); err != nil {
		return err
	}

	_, err := fmt.Fprintf(stdout, "applied branch protection to %s/branches/%s\n", repo, branch)
	if err != nil {
		return fmt.Errorf("writing apply confirmation: %w", err)
	}
	return nil
}
