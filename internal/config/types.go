package config

import (
	"fmt"
	"sort"
	"strings"
)

// CICDFile represents the .github/cicd.yaml file structure
// Contains both config (pipeline definition) and state (deployment tracking)
type CICDFile struct {
	Config        *TrunkConfig         `yaml:"config" json:"config"`
	State         map[string]*EnvState `yaml:"state,omitempty" json:"state,omitempty"`
	LatestRelease *LatestReleaseState  `yaml:"latest_release,omitempty" json:"latest_release,omitempty"`
}

// LatestReleaseState tracks the most recent published release
type LatestReleaseState struct {
	Version    string `yaml:"version,omitempty" json:"version,omitempty"`         // e.g., v1.2.0
	SHA        string `yaml:"sha,omitempty" json:"sha,omitempty"`                 // Commit SHA
	ReleasedOn string `yaml:"released_on,omitempty" json:"released_on,omitempty"` // ISO 8601 timestamp
	ReleasedBy string `yaml:"released_by,omitempty" json:"released_by,omitempty"` // GitHub actor who triggered release
	// Components is the reserved per-component latest-release record (#176).
	Components map[string]*ComponentReleaseState `yaml:"components,omitempty" json:"components,omitempty"`
}

// EnvState tracks the state of a single environment
type EnvState struct {
	SHA         string                          `yaml:"sha,omitempty" json:"sha,omitempty"`
	Version     string                          `yaml:"version,omitempty" json:"version,omitempty"` // Semantic version tag (e.g., v1.2.3-rc.0)
	CommittedAt string                          `yaml:"committed_at,omitempty" json:"committed_at,omitempty"`
	CommittedBy string                          `yaml:"committed_by,omitempty" json:"committed_by,omitempty"`
	Builds      map[string]*BuildState          `yaml:"builds,omitempty" json:"builds,omitempty"`
	Deploys     map[string]*DeployState         `yaml:"deploys,omitempty" json:"deploys,omitempty"`
	External    map[string]*ExternalDeployState `yaml:"external,omitempty" json:"external,omitempty"` // External repo deploy states
	// Ref is the integration branch this environment tracks instead of trunk
	// (e.g., a hotfix branch). Empty means the environment tracks trunk.
	Ref string `yaml:"ref,omitempty" json:"ref,omitempty"`
	// BaseSHA is the trunk commit the integration branch diverged from.
	BaseSHA string `yaml:"base_sha,omitempty" json:"base_sha,omitempty"`
	// Patches lists the patch commit SHAs applied on top of BaseSHA.
	Patches []string `yaml:"patches,omitempty" json:"patches,omitempty"`
	// Previous is the per-environment deploy-history ring (#23): snapshots of
	// prior env states, newest first, bounded to MaxPreviousSnapshots. Populated
	// on every state transition via PushPreviousSnapshot.
	Previous []EnvStateSnapshot `yaml:"previous,omitempty" json:"previous,omitempty"`
	// Components is the reserved per-component state slot (#176): per-component
	// version/sha records keyed by component name. Reserved-shape only.
	Components map[string]*ComponentState `yaml:"components,omitempty" json:"components,omitempty"`
}

// IsDiverged reports whether the environment is on an integration branch rather
// than tracking trunk: true when it has a custom Ref or has Patches applied.
func (s *EnvState) IsDiverged() bool {
	if s == nil {
		return false
	}
	return s.Ref != "" || len(s.Patches) > 0
}

// EnvStateSnapshot is a single prior env-state entry in the deploy-history
// ring (state.<env>.previous).
type EnvStateSnapshot struct {
	SHA         string `yaml:"sha,omitempty" json:"sha,omitempty"`
	Version     string `yaml:"version,omitempty" json:"version,omitempty"`
	CommittedAt string `yaml:"committed_at,omitempty" json:"committed_at,omitempty"`
	CommittedBy string `yaml:"committed_by,omitempty" json:"committed_by,omitempty"`
}

// BuildState tracks the state of a single build within an environment
type BuildState struct {
	SHA        string            `yaml:"sha,omitempty" json:"sha,omitempty"`
	BuiltAt    string            `yaml:"built_at,omitempty" json:"built_at,omitempty"`
	BuiltBy    string            `yaml:"built_by,omitempty" json:"built_by,omitempty"`
	ArtifactID string            `yaml:"artifact_id,omitempty" json:"artifact_id,omitempty"` // Immutable artifact identifier from build output (e.g., Docker image digest)
	Tags       map[string]string `yaml:"tags,omitempty" json:"tags,omitempty"`               // state_tags values
}

// DeployState tracks the state of a single deployable within an environment
type DeployState struct {
	SHA        string            `yaml:"sha,omitempty" json:"sha,omitempty"`
	Version    string            `yaml:"version,omitempty" json:"version,omitempty"` // Version this deployable deployed (reserved-shape; independent of env-level Version)
	DeployedAt string            `yaml:"deployed_at,omitempty" json:"deployed_at,omitempty"`
	DeployedBy string            `yaml:"deployed_by,omitempty" json:"deployed_by,omitempty"`
	Tags       map[string]string `yaml:"tags,omitempty" json:"tags,omitempty"`             // state_tags values
	TargetSHA  string            `yaml:"target_sha,omitempty" json:"target_sha,omitempty"` // RESERVED - reconciled GitOps-repo HEAD SHA, so a future implementation can key promotion off it. Not yet wired.
}

// ExternalDeployState tracks the state of an external deployable within an environment
type ExternalDeployState struct {
	Repo       string            `yaml:"repo" json:"repo"`                                   // Source repo (e.g., "org/cdk-infra")
	SHA        string            `yaml:"sha,omitempty" json:"sha,omitempty"`                 // Commit SHA from external repo
	Version    string            `yaml:"version,omitempty" json:"version,omitempty"`         // Version from external repo
	DeployedAt string            `yaml:"deployed_at,omitempty" json:"deployed_at,omitempty"` // Last deployment timestamp
	DeployedBy string            `yaml:"deployed_by,omitempty" json:"deployed_by,omitempty"` // Actor who triggered deploy
	Artifacts  map[string]string `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`     // Artifact references (e.g., image_tag)
}

// Schema version bounds. schema_version declares which generation of the
// manifest schema a config is written for. It is a single monotonic integer
// (a "schema major"), not a semver string: additive changes (new optional
// fields, new enum values) never bump it, because old manifests keep working
// and the CLI fills defaults. Only a breaking change (a removed field, a
// changed default's behavior, a re-typed field) bumps it, with a matching
// migration entry in CHANGELOG.md. See docs/versioning.md for the policy.
const (
	// CurrentSchemaVersion is the highest schema version this CLI understands
	// and the version assumed for manifests that omit schema_version.
	CurrentSchemaVersion = 1
	// MinSchemaVersion is the oldest schema version this CLI still reads.
	// Manifests below this are rejected with a pointer to the migration table.
	MinSchemaVersion = 1
)

// TrunkConfig represents the pipeline configuration (within config: section)
type TrunkConfig struct {
	SchemaVersion int      `yaml:"schema_version,omitempty" json:"schema_version,omitempty"` // Manifest schema generation (default: CurrentSchemaVersion when omitted)
	TrunkBranch   string   `yaml:"trunk_branch" json:"trunk_branch"`
	Triggers      []string `yaml:"triggers,omitempty" json:"triggers,omitempty"`           // Global triggers for orchestration workflow paths filter
	// ReleaseTrigger selects how the generated orchestrate workflow fires.
	// "" or "push" (default) keeps the push-on-trunk + workflow_dispatch
	// triggers. "dispatch" drops the push: trigger so orchestrate runs only on
	// workflow_dispatch, letting a maintainer-owned gate decide when a release
	// candidate is cut. Opt-in; repos that do not set it keep push triggers.
	ReleaseTrigger string `yaml:"release_trigger,omitempty" json:"release_trigger,omitempty"`
	Environments  []string `yaml:"environments,omitempty" json:"environments,omitempty"`   // Empty = no-environment setup (library/CLI projects)
	CLIVersion    string   `yaml:"cli_version,omitempty" json:"cli_version,omitempty"`     // cascade CLI version (e.g., v1.0.0)
	// CLIVersionSHA is the 40-hex commit SHA that cli_version resolves to. When
	// set and pin_mode is sha, generated setup-cli self-action refs are pinned to
	// this commit with cli_version carried as a trailing comment; otherwise the
	// version tag is emitted (today's behavior). The bump automation peels the
	// annotated cli_version tag to its commit and writes this.
	CLIVersionSHA string `yaml:"cli_version_sha,omitempty" json:"cli_version_sha,omitempty"`
	TagPrefix     string   `yaml:"tag_prefix,omitempty" json:"tag_prefix,omitempty"`       // Version tag prefix (default: "v")
	ReleaseToken  string   `yaml:"release_token,omitempty" json:"release_token,omitempty"` // GitHub secret name for release operations (default: "GITHUB_TOKEN")
	StateToken    string   `yaml:"state_token,omitempty" json:"state_token,omitempty"`     // Token expression for writing manifest state to the trunk branch (default: "GITHUB_TOKEN")
	// ReleaseTokenApp optionally backs the release-token seam with a GitHub App
	// identity instead of a static secret. When set, the generator mints a
	// short-lived installation token at run time and the release steps consume it.
	ReleaseTokenApp *AppTokenSource `yaml:"release_token_app,omitempty" json:"release_token_app,omitempty"`
	// StateTokenApp optionally backs the state-token seam with a GitHub App
	// identity instead of a static secret. When set, the generator mints a
	// short-lived installation token at run time and the state-write steps consume it.
	StateTokenApp *AppTokenSource      `yaml:"state_token_app,omitempty" json:"state_token_app,omitempty"`
	ManifestFile  string               `yaml:"manifest_file,omitempty" json:"manifest_file,omitempty"` // Config file path (default: ".github/manifest.yaml")
	ManifestKey   string               `yaml:"manifest_key,omitempty" json:"manifest_key,omitempty"`   // Nested key in manifest file (default: "ci")
	ActionFolder  string               `yaml:"action_folder,omitempty" json:"action_folder,omitempty"` // Folder name for manage-release action (default: "manage-release")
	Git           *GitConfig           `yaml:"git,omitempty" json:"git,omitempty"`
	Validate      *ValidateConfig      `yaml:"validate,omitempty" json:"validate,omitempty"`
	Builds        []BuildConfig        `yaml:"builds,omitempty" json:"builds,omitempty"`     // Empty = no orchestrated builds
	Deploys       []DeployConfig       `yaml:"deploys,omitempty" json:"deploys,omitempty"`   // Empty = no deploys (library/CLI projects)
	Publish       *PublishConfig       `yaml:"publish,omitempty" json:"publish,omitempty"`   // Optional: retag artifacts after a release is published
	External      []ExternalRepoConfig `yaml:"external,omitempty" json:"external,omitempty"` // External repos this primary coordinates
	Notify        *NotifyConfig        `yaml:"notify,omitempty" json:"notify,omitempty"`     // Satellite: notify primary after dev deploy
	Release       *ReleaseConfig       `yaml:"release,omitempty" json:"release,omitempty"`
	Changelog     *ChangelogConfig     `yaml:"changelog,omitempty" json:"changelog,omitempty"`
	Concurrency   *ConcurrencyConfig   `yaml:"concurrency,omitempty" json:"concurrency,omitempty"` // Optional: top-level concurrency: block on the orchestrate workflow

	// v1 reserved-shape config-level fields (parse + structural validation only).
	RunsOn            *RunsOn                  `yaml:"runs_on,omitempty" json:"runs_on,omitempty"`                         // Default runner for cascade-owned jobs
	JobTimeoutMinutes int                      `yaml:"job_timeout_minutes,omitempty" json:"job_timeout_minutes,omitempty"` // Default timeout-minutes for cascade-owned jobs
	DispatchInputs    map[string]DispatchInput `yaml:"dispatch_inputs,omitempty" json:"dispatch_inputs,omitempty"`         // Operator-facing manual-run inputs
	ExtraTriggers     *ExtraTriggers           `yaml:"extra_triggers,omitempty" json:"extra_triggers,omitempty"`           // Non-push trigger types
	PRPreview         *PRPreviewConfig         `yaml:"pr_preview,omitempty" json:"pr_preview,omitempty"`
	ValidateCheck     *ValidateCheckConfig     `yaml:"validate_check,omitempty" json:"validate_check,omitempty"`
	MergeQueue        *MergeQueueConfig        `yaml:"merge_queue,omitempty" json:"merge_queue,omitempty"`
	DriftCheck        *DriftCheckConfig        `yaml:"drift_check,omitempty" json:"drift_check,omitempty"` // Opt-in workflow drift-check PR lane (#229)
	// Rollback configures the opt-in rollback workflow. Absent by default; when
	// set with repository_dispatch, an external signal can fire the rollback (#181).
	Rollback *RollbackConfig `yaml:"rollback,omitempty" json:"rollback,omitempty"`
	// Deployments configures opt-in GitHub Deployments API integration.
	Deployments       *DeploymentsConfig           `yaml:"deployments,omitempty" json:"deployments,omitempty"`
	PinMode           string                       `yaml:"pin_mode,omitempty" json:"pin_mode,omitempty"` // tag | sha (default tag)
	ActionPins        map[string]string            `yaml:"action_pins,omitempty" json:"action_pins,omitempty"`
	Telemetry         *TelemetryConfig             `yaml:"telemetry,omitempty" json:"telemetry,omitempty"`
	EnvironmentConfig map[string]EnvironmentConfig `yaml:"environment_config,omitempty" json:"environment_config,omitempty"`
	// Components reserves the shape for independently versioned components sharing
	// one manifest (#176). v1 contract: parse + structural validation only; no
	// generator, state, or runtime behavior is attached. Absent by default.
	Components map[string]ComponentConfig `yaml:"components,omitempty" json:"components,omitempty"`
}

// ConcurrencyConfig overrides the default concurrency: block emitted on the
// generated orchestrate workflow. Defaults: group=orchestrate-${{ github.ref }},
// cancel-in-progress=true. Set cancel_in_progress=false to queue runs instead
// of cancelling older ones.
type ConcurrencyConfig struct {
	Group            string `yaml:"group,omitempty" json:"group,omitempty"`
	CancelInProgress bool   `yaml:"cancel_in_progress" json:"cancel_in_progress"`
}

// OrchestrateDispatchOnly reports whether the generated orchestrate workflow
// should omit its push: trigger and run only on workflow_dispatch.
func (c *TrunkConfig) OrchestrateDispatchOnly() bool {
	return c.ReleaseTrigger == ReleaseTriggerDispatch
}

// GetConcurrencyGroup returns the configured group expression or the default.
func (c *TrunkConfig) GetConcurrencyGroup() string {
	if c.Concurrency != nil && c.Concurrency.Group != "" {
		return c.Concurrency.Group
	}
	return "orchestrate-${{ github.ref }}"
}

// GetConcurrencyCancelInProgress returns whether to cancel older in-progress
// runs. Defaults to true (newer push obsoletes older). Returns false only when
// explicitly configured.
func (c *TrunkConfig) GetConcurrencyCancelInProgress() bool {
	if c.Concurrency == nil {
		return true
	}
	return c.Concurrency.CancelInProgress
}

// GetSchemaVersion returns the effective schema version: the configured value,
// or CurrentSchemaVersion when omitted (schema_version == 0). An omitted version
// is treated as the current generation for all downstream logic; ValidateSchemaVersion
// surfaces the accompanying "no schema_version" warning.
func (c *TrunkConfig) GetSchemaVersion() int {
	if c.SchemaVersion == 0 {
		return CurrentSchemaVersion
	}
	return c.SchemaVersion
}

// validateSchemaVersion is the testable core of the compatibility check. It
// evaluates v against the provided min and current bounds and returns a warning
// string (non-empty means warn-and-accept) or an error (means reject). Callers
// that need to exercise the full matrix (including branches that are currently
// unreachable when min == current) should call this directly.
//
// Rules:
//
//   - v == 0              → warn "omitted; assuming current"
//   - v < 0              → error "must be a positive integer"
//   - v > current        → error "upgrade the CLI"
//   - v < min (and != 0) → error "no longer supported"
//   - min <= v < current → warn "this CLI supports schema versions up to N and still reads version M"
//   - v == current       → "" (silent accept)
func validateSchemaVersion(v, min, current int) (warning string, err error) {
	switch {
	case v == 0:
		return fmt.Sprintf(
			"manifest has no schema_version; assuming %d. Pin it explicitly (schema_version: %d).",
			current, current), nil
	case v < 0:
		return "", fmt.Errorf("schema_version must be a positive integer, got %d", v)
	case v > current:
		return "", fmt.Errorf(
			"manifest requires schema version %d but this CLI supports schema versions up to %d; "+
				"upgrade the CLI (cli_version); see docs/versioning.md", v, current)
	case v < min:
		return "", fmt.Errorf(
			"manifest schema version %d is no longer supported (minimum %d); "+
				"see the migration table in CHANGELOG.md / docs/versioning.md", v, min)
	case v < current:
		return fmt.Sprintf(
			"this CLI supports schema versions up to %d and still reads version %d; "+
				"see the migration table in docs/versioning.md.", current, v), nil
	default: // v == current
		return "", nil
	}
}

// ValidateSchemaVersion checks the manifest's schema_version against the CLI's
// supported range and returns (warnings, fatalErr). It enforces the
// compatibility contract documented in docs/versioning.md:
//
//   - omitted (0)                        → accept, warn (assume current)
//   - MinSchemaVersion..Current-1        → accept, warn (older but supported)
//   - == CurrentSchemaVersion            → accept silently
//   - < MinSchemaVersion (and != 0)      → reject (too old; migrate)
//   - > CurrentSchemaVersion             → reject (newer; upgrade CLI)
//   - negative                           → reject (invalid)
//
// A non-nil fatalErr means the manifest must not be used for generation.
func (c *TrunkConfig) ValidateSchemaVersion() (warnings []string, fatalErr error) {
	warn, err := validateSchemaVersion(c.SchemaVersion, MinSchemaVersion, CurrentSchemaVersion)
	if err != nil {
		return nil, err
	}
	if warn != "" {
		return []string{warn}, nil
	}
	return nil, nil
}

// DefaultCLIVersion is the immutable cascade release tag pinned into generated
// workflows when cli_version is unset (or set to the mutable "latest"). Bump this
// with each cascade release so generated pipelines track the newest stable tag.
const DefaultCLIVersion = "v0.1.0"

// GetCLIVersion returns the configured CLI version, resolving mutable refs to the
// immutable pinned default for supply-chain integrity.
// Supported values:
//   - "" or "latest" → DefaultCLIVersion (the pinned, immutable release tag)
//   - "beta" → passes through; the generator maps it to "master" (bleeding edge)
//   - "vX.Y.Z" → passes through as a specific version tag
func (c *TrunkConfig) GetCLIVersion() string {
	switch c.CLIVersion {
	case "", "latest":
		return DefaultCLIVersion
	default:
		return c.CLIVersion
	}
}

// GetTagPrefix returns the configured tag prefix or "v" if not specified
func (c *TrunkConfig) GetTagPrefix() string {
	if c.TagPrefix == "" {
		return "v"
	}
	return c.TagPrefix
}

// normalizeTokenExpression returns a GitHub Actions expression that resolves to
// a token at run time. It accepts either a full expression
// ("${{ secrets.MY_TOKEN }}"), an unwrapped context form ("secrets.MY_TOKEN",
// "vars.MY_TOKEN"), or a bare secret name ("MY_TOKEN") and always returns a
// wrapped "${{ ... }}" expression. A bare name is treated as a secret, matching
// the documented "GitHub secret name" intent of the token fields. This prevents
// a bare name from being emitted verbatim into a workflow, which would resolve
// to a literal string token and fail authentication.
func normalizeTokenExpression(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	// Already a full expression: leave it untouched.
	if strings.HasPrefix(trimmed, "${{") && strings.HasSuffix(trimmed, "}}") {
		return trimmed
	}
	// Unwrapped context form (secrets.X, vars.X, env.X, ...): wrap it.
	if i := strings.IndexByte(trimmed, '.'); i > 0 {
		switch trimmed[:i] {
		case "secrets", "vars", "env", "inputs", "github":
			return "${{ " + trimmed + " }}"
		}
	}
	// Bare name: treat it as a secret.
	return "${{ secrets." + trimmed + " }}"
}

// GetReleaseToken returns the configured release token as a resolvable GitHub
// Actions expression. A bare secret name (e.g. "MY_TOKEN") is normalized to
// "${{ secrets.MY_TOKEN }}".
//
// When release_token is unset, the result depends on state_token. The release
// token creates the rc tag, and creating a tag that must trigger downstream
// workflows (Release, the fleet, promotion) requires a trigger-capable token:
// GitHub deliberately suppresses workflow triggers for ref creations made with
// the default GITHUB_TOKEN. So when an adopter has supplied a trigger-capable
// state_token (typically a PAT or App-backed token for protected-trunk writes)
// but left release_token unset, defaulting the release token to GITHUB_TOKEN
// would silently break the rc-to-release chain: the rc tag fires nothing.
//
// To avoid that silent dead-chain, an unset release_token falls back to the
// state token expression whenever state_token is set, reusing the trigger-
// capable token the adopter already configured. When both are unset, the
// historical "${{ secrets.GITHUB_TOKEN }}" default is preserved for
// back-compatibility (a single-token, same-repo setup does not need the
// chain). When release_token is set, it always wins.
//
// Note on App-backed state tokens: a minted App token lives in a generator
// step output, not in config, so this config-layer resolver can only fall back
// to the static state_token string. An adopter whose state token is supplied
// solely via state_token_app (no static state_token) keeps the GITHUB_TOKEN
// default here; to arm the rc-to-release chain in that shape, set a static,
// trigger-capable release_token explicitly.
func (c *TrunkConfig) GetReleaseToken() string {
	if c.ReleaseToken == "" {
		if c.StateToken != "" {
			return normalizeTokenExpression(c.StateToken)
		}
		return "${{ secrets.GITHUB_TOKEN }}"
	}
	return normalizeTokenExpression(c.ReleaseToken)
}

// GetStateToken returns the configured state-write token expression or
// "${{ secrets.GITHUB_TOKEN }}" if not specified. This token is used to write
// the manifest state back to the trunk branch. On real GitHub the write goes
// through the REST API, so a token with permission to bypass branch protection
// (for example a GitHub App or bot token) can be supplied here to update a
// protected trunk and produce a verified, signed commit.
// Users should provide the full GitHub Actions expression, e.g. "${{ secrets.MY_TOKEN }}".
func (c *TrunkConfig) GetStateToken() string {
	if c.StateToken == "" {
		return "${{ secrets.GITHUB_TOKEN }}"
	}
	return normalizeTokenExpression(c.StateToken)
}

// AppTokenSource backs a token seam with a GitHub App identity. At run time the
// generator emits an actions/create-github-app-token step that exchanges these
// for a short-lived installation token; the consuming steps reference that
// minted token instead of a static secret. AppID and PrivateKey are SECRET
// REFERENCES (a secrets/vars expression or bare secret name), never raw key
// material: the App private key is stored only as a GitHub secret.
type AppTokenSource struct {
	AppID      string `yaml:"app_id,omitempty" json:"app_id,omitempty"`
	PrivateKey string `yaml:"private_key,omitempty" json:"private_key,omitempty"`
}

// HasReleaseTokenApp reports whether a GitHub App identity backs the release-token seam.
func (c *TrunkConfig) HasReleaseTokenApp() bool { return c.ReleaseTokenApp != nil }

// HasStateTokenApp reports whether a GitHub App identity backs the state-token seam.
func (c *TrunkConfig) HasStateTokenApp() bool { return c.StateTokenApp != nil }

// GetReleaseTokenAppID returns the release App's app_id as a resolvable GitHub
// Actions expression (a bare name becomes "${{ secrets.NAME }}"), or "" when no
// release App identity is configured.
func (c *TrunkConfig) GetReleaseTokenAppID() string {
	if c.ReleaseTokenApp == nil {
		return ""
	}
	return normalizeTokenExpression(c.ReleaseTokenApp.AppID)
}

// GetReleaseTokenAppPrivateKey returns the release App's private_key as a
// resolvable GitHub Actions expression, or "" when no release App identity is
// configured. The value is a secret reference, never raw key material.
func (c *TrunkConfig) GetReleaseTokenAppPrivateKey() string {
	if c.ReleaseTokenApp == nil {
		return ""
	}
	return normalizeTokenExpression(c.ReleaseTokenApp.PrivateKey)
}

// GetStateTokenAppID returns the state App's app_id as a resolvable GitHub
// Actions expression, or "" when no state App identity is configured.
func (c *TrunkConfig) GetStateTokenAppID() string {
	if c.StateTokenApp == nil {
		return ""
	}
	return normalizeTokenExpression(c.StateTokenApp.AppID)
}

// GetStateTokenAppPrivateKey returns the state App's private_key as a resolvable
// GitHub Actions expression, or "" when no state App identity is configured. The
// value is a secret reference, never raw key material.
func (c *TrunkConfig) GetStateTokenAppPrivateKey() string {
	if c.StateTokenApp == nil {
		return ""
	}
	return normalizeTokenExpression(c.StateTokenApp.PrivateKey)
}

// GetManifestFile returns the configured manifest file path or ".github/manifest.yaml" if not specified
func (c *TrunkConfig) GetManifestFile() string {
	if c.ManifestFile == "" {
		return ".github/manifest.yaml"
	}
	return c.ManifestFile
}

// GetManifestKey returns the configured manifest key or "ci" if not specified
func (c *TrunkConfig) GetManifestKey() string {
	if c.ManifestKey == "" {
		return "ci"
	}
	return c.ManifestKey
}

// GetActionFolder returns the configured action folder name or "manage-release" if not specified
func (c *TrunkConfig) GetActionFolder() string {
	if c.ActionFolder == "" {
		return "manage-release"
	}
	return c.ActionFolder
}

// GitConfig defines git identity and signing configuration
type GitConfig struct {
	Mode         string `yaml:"mode,omitempty" json:"mode,omitempty"`                     // default, custom, external
	UserName     string `yaml:"user_name,omitempty" json:"user_name,omitempty"`           // git user.name (if mode=custom)
	UserEmail    string `yaml:"user_email,omitempty" json:"user_email,omitempty"`         // git user.email (if mode=custom)
	GPGKeyID     string `yaml:"gpg_key_id,omitempty" json:"gpg_key_id,omitempty"`         // GPG key ID for signing
	GPGKeySecret string `yaml:"gpg_key_secret,omitempty" json:"gpg_key_secret,omitempty"` // secret name containing GPG key
}

// GitMode constants
const (
	GitModeDefault  = "default"  // Use github-actions[bot]
	GitModeCustom   = "custom"   // Use configured user_name/user_email
	GitModeExternal = "external" // Skip git config, assume pre-configured
)

// GetGitMode returns the effective git mode (default if not specified)
func (c *TrunkConfig) GetGitMode() string {
	if c.Git == nil || c.Git.Mode == "" {
		return GitModeDefault
	}
	return c.Git.Mode
}

// GetGitUserName returns the git user.name to use
func (c *TrunkConfig) GetGitUserName() string {
	if c.Git != nil && c.Git.UserName != "" {
		return c.Git.UserName
	}
	return "github-actions[bot]"
}

// GetGitUserEmail returns the git user.email to use
func (c *TrunkConfig) GetGitUserEmail() string {
	if c.Git != nil && c.Git.UserEmail != "" {
		return c.Git.UserEmail
	}
	return "github-actions[bot]@users.noreply.github.com"
}

// HasGPGSigning returns true if GPG signing is configured
func (c *TrunkConfig) HasGPGSigning() bool {
	return c.Git != nil && c.Git.GPGKeyID != "" && c.Git.GPGKeySecret != ""
}

// ValidateConfig defines a validation workflow
type ValidateConfig struct {
	Workflow       string                            `yaml:"workflow,omitempty" json:"workflow,omitempty"`
	Run            string                            `yaml:"run,omitempty" json:"run,omitempty"`           // rejected: inline run is no longer supported; use workflow:
	Shell          string                            `yaml:"shell,omitempty" json:"shell,omitempty"`       // rejected: inline shell is no longer supported; use workflow:
	Triggers       []string                          `yaml:"triggers,omitempty" json:"triggers,omitempty"` // File patterns that should trigger validation
	SupportsDryRun bool                              `yaml:"supports_dry_run,omitempty" json:"supports_dry_run,omitempty"`
	Inputs         map[string]interface{}            `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	EnvInputs      map[string]map[string]interface{} `yaml:"env_inputs,omitempty" json:"env_inputs,omitempty"`
	RunPolicy      string                            `yaml:"run_policy,omitempty" json:"run_policy,omitempty"`
	OnFailure      string                            `yaml:"on_failure,omitempty" json:"on_failure,omitempty"`
	Retries        int                               `yaml:"retries,omitempty" json:"retries,omitempty"`
	TimeoutMinutes int                               `yaml:"timeout_minutes,omitempty" json:"timeout_minutes,omitempty"` // Job-level timeout-minutes (omits when 0)

	// v1 reserved-shape per-callback fields (parse + structural validation only).
	// The validate gate is a singleton, so the spec scopes optional_depends_on
	// (§2.11) and auto_commits (§5.5) to builds/deploys only; not here.
	Secrets     *SecretsConfig     `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Permissions map[string]string  `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	RunsOn      *RunsOn            `yaml:"runs_on,omitempty" json:"runs_on,omitempty"`
	Concurrency *ConcurrencyConfig `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
}

// BuildConfig defines a build target
type BuildConfig struct {
	Name           string                            `yaml:"name" json:"name"`
	Workflow       string                            `yaml:"workflow,omitempty" json:"workflow,omitempty"`
	Run            string                            `yaml:"run,omitempty" json:"run,omitempty"`     // rejected: inline run is no longer supported; use workflow:
	Shell          string                            `yaml:"shell,omitempty" json:"shell,omitempty"` // rejected: inline shell is no longer supported; use workflow:
	Triggers       []string                          `yaml:"triggers" json:"triggers"`
	DependsOn      []string                          `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	StateTags      []string                          `yaml:"state_tags,omitempty" json:"state_tags,omitempty"`
	Artifacts      []ArtifactConfig                  `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
	RunPolicy      string                            `yaml:"run_policy,omitempty" json:"run_policy,omitempty"`
	OnFailure      string                            `yaml:"on_failure,omitempty" json:"on_failure,omitempty"`
	Retries        int                               `yaml:"retries,omitempty" json:"retries,omitempty"`
	TimeoutMinutes int                               `yaml:"timeout_minutes,omitempty" json:"timeout_minutes,omitempty"` // Job-level timeout-minutes (omits when 0)
	Inputs         map[string]interface{}            `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	EnvInputs      map[string]map[string]interface{} `yaml:"env_inputs,omitempty" json:"env_inputs,omitempty"`

	// v1 reserved-shape per-callback fields (parse + structural validation only).
	Secrets             *SecretsConfig       `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Permissions         map[string]string    `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	RunsOn              *RunsOn              `yaml:"runs_on,omitempty" json:"runs_on,omitempty"`
	Concurrency         *ConcurrencyConfig   `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
	Matrix              *MatrixConfig        `yaml:"matrix,omitempty" json:"matrix,omitempty"` // Build fan-out (builds only)
	OptionalDependsOn   []string             `yaml:"optional_depends_on,omitempty" json:"optional_depends_on,omitempty"`
	AutoCommits         bool                 `yaml:"auto_commits,omitempty" json:"auto_commits,omitempty"`
	PassthroughArtifact *PassthroughArtifact `yaml:"artifact,omitempty" json:"artifact,omitempty"` // GHA artifact passing within an orchestrate run (#16)
}

// ArtifactConfig defines a release artifact produced by a build
// Build workflows should upload artifacts with name: release-{build-name}-{artifact-name}
type ArtifactConfig struct {
	Name     string `yaml:"name" json:"name"`                             // Artifact identifier (e.g., "linux-amd64", "checksums")
	Path     string `yaml:"path" json:"path"`                             // Glob pattern for files to include (e.g., "dist/*.tar.gz")
	Required bool   `yaml:"required,omitempty" json:"required,omitempty"` // Fail release if artifact missing (default: true)
}

// PassthroughArtifact declares GHA artifacts passed between jobs within a single
// orchestrate run via actions/upload-artifact and actions/download-artifact.
// The uploaded artifact is named "build-{build-name}" so consumers can reference it.
//
// Example:
//
//	builds:
//	  - name: compile
//	    artifact:
//	      upload: dist/
//	  - name: sign
//	    depends_on: [compile]
//	    artifact:
//	      downloads: [compile]   # downloads "build-compile" before running
//	      upload: dist-signed/
type PassthroughArtifact struct {
	// Upload is the path glob to upload after this job completes (via upload-artifact).
	// The artifact is named "build-{this-job-name}".
	Upload string `yaml:"upload,omitempty" json:"upload,omitempty"`
	// Downloads lists the names of upstream build jobs whose artifacts to download
	// before this job runs (each downloads "build-{name}").
	Downloads []string `yaml:"downloads,omitempty" json:"downloads,omitempty"`
}

// DeployConfig defines a deployment target
type DeployConfig struct {
	Name           string                            `yaml:"name" json:"name"`
	Workflow       string                            `yaml:"workflow,omitempty" json:"workflow,omitempty"`
	Run            string                            `yaml:"run,omitempty" json:"run,omitempty"`     // rejected: inline run is no longer supported; use workflow:
	Shell          string                            `yaml:"shell,omitempty" json:"shell,omitempty"` // rejected: inline shell is no longer supported; use workflow:
	Triggers       []string                          `yaml:"triggers" json:"triggers"`
	DependsOn      []string                          `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	StateTags      []string                          `yaml:"state_tags,omitempty" json:"state_tags,omitempty"`
	SupportsDryRun bool                              `yaml:"supports_dry_run,omitempty" json:"supports_dry_run,omitempty"`
	RunPolicy      string                            `yaml:"run_policy,omitempty" json:"run_policy,omitempty"`
	OnFailure      string                            `yaml:"on_failure,omitempty" json:"on_failure,omitempty"`
	Retries        int                               `yaml:"retries,omitempty" json:"retries,omitempty"`
	TimeoutMinutes int                               `yaml:"timeout_minutes,omitempty" json:"timeout_minutes,omitempty"` // Job-level timeout-minutes (omits when 0)
	Inputs         map[string]interface{}            `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	EnvInputs      map[string]map[string]interface{} `yaml:"env_inputs,omitempty" json:"env_inputs,omitempty"`

	// v1 reserved-shape per-callback fields (parse + structural validation only).
	Secrets             *SecretsConfig       `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Permissions         map[string]string    `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	RunsOn              *RunsOn              `yaml:"runs_on,omitempty" json:"runs_on,omitempty"`
	Concurrency         *ConcurrencyConfig   `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
	Rollout             *RolloutConfig       `yaml:"rollout,omitempty" json:"rollout,omitempty"` // Deploy rollout strategy (deploys only)
	DeployTarget        *DeployTarget        `yaml:"deploy_target,omitempty" json:"deploy_target,omitempty"`
	OptionalDependsOn   []string             `yaml:"optional_depends_on,omitempty" json:"optional_depends_on,omitempty"`
	AutoCommits         bool                 `yaml:"auto_commits,omitempty" json:"auto_commits,omitempty"`
	PassthroughArtifact *PassthroughArtifact `yaml:"artifact,omitempty" json:"artifact,omitempty"` // GHA artifact passing within an orchestrate run (#16)
}

// PublishConfig defines a publish callback invoked after a release is published.
// The publish workflow receives one matrix entry per configured build, carrying
// the build's metadata so the user can retag artifacts in their registries.
//
// Inputs passed to the publish workflow per build:
//   - build_name:   name of the build (e.g., "app")
//   - old_version:  the RC version that was published (e.g., "v1.0.0-rc.2")
//   - new_version:  the final semver version (e.g., "v1.0.0")
//   - sha:          the git commit SHA of the built artifact
//   - artifact_id:  the immutable artifact identifier stored from the build job
//     output (e.g., Docker image digest "sha256:abc..."). Empty if
//     the build did not emit an artifact_id output.
type PublishConfig struct {
	Workflow string `yaml:"workflow" json:"workflow"` // Path to the reusable workflow (e.g., ".github/workflows/publish.yaml")
}

// ReleaseConfig defines release management settings
type ReleaseConfig struct {
	Disabled bool   `yaml:"disabled,omitempty" json:"disabled,omitempty"` // true = disabled
	Tag      string `yaml:"tag,omitempty" json:"tag,omitempty"`           // callback.output reference for external releases

	// Workflow is the optional reusable-workflow path dispatched after a final
	// release is published, for example to build and attach release binaries to
	// the published release (e.g. ".github/workflows/release-build.yaml"). When
	// unset, no release-build dispatch is emitted and publishing a release does
	// not trigger any follow-on workflow.
	Workflow string `yaml:"workflow,omitempty" json:"workflow,omitempty"`

	// VersionOverrides reserves the addressing pointer for maintainer-committed
	// version-intent override files. RESERVED - parse + structural validation
	// only; no generator/state/runtime behavior. Absent by default.
	VersionOverrides *VersionOverridesConfig `yaml:"version_overrides,omitempty" json:"version_overrides,omitempty"`
}

// VersionOverridesConfig is the reserved pointer to the override-file location.
// Only the addressing shape is frozen in v1; file format and the
// fold-into-version-calculation behavior land post-1.0 additively. Any future
// override values map onto the canonical version primitives in
// internal/version (BumpType: BumpNone/BumpPatch/BumpMinor/BumpMajor and the
// PreRelease line); this reservation introduces no parallel enum.
type VersionOverridesConfig struct {
	// Dir is the directory holding override files. Relative, no ".." segments.
	// Empty => the implementation default (reserved).
	Dir string `yaml:"dir,omitempty" json:"dir,omitempty"`
}

// ExternalRepoConfig defines an external repository that this primary coordinates
type ExternalRepoConfig struct {
	Repo    string                 `yaml:"repo" json:"repo"`                   // e.g., "org/cdk-infra"
	Ref     string                 `yaml:"ref,omitempty" json:"ref,omitempty"` // Branch/tag reference (default: trunk_branch)
	Deploys []ExternalDeployConfig `yaml:"deploys" json:"deploys"`             // Deployables from this repo
}

// ExternalDeployConfig defines a deployable from an external repository
type ExternalDeployConfig struct {
	Name     string   `yaml:"name" json:"name"`                             // Deploy identifier (e.g., "cdk")
	Workflow string   `yaml:"workflow,omitempty" json:"workflow,omitempty"` // Workflow path - local (.github/...) or external (org/repo/.github/...@ref)
	Run      string   `yaml:"run,omitempty" json:"run,omitempty"`           // rejected: inline run is no longer supported; use workflow:
	Shell    string   `yaml:"shell,omitempty" json:"shell,omitempty"`       // rejected: inline shell is no longer supported; use workflow:
	Triggers []string `yaml:"triggers,omitempty" json:"triggers,omitempty"` // File patterns for change detection

	// v1 reserved-shape per-callback fields (parse + structural validation only).
	// Applied by extension to external deploys per spec §2.
	Secrets           *SecretsConfig     `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Permissions       map[string]string  `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	RunsOn            *RunsOn            `yaml:"runs_on,omitempty" json:"runs_on,omitempty"`
	Concurrency       *ConcurrencyConfig `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
	Rollout           *RolloutConfig     `yaml:"rollout,omitempty" json:"rollout,omitempty"`
	DeployTarget      *DeployTarget      `yaml:"deploy_target,omitempty" json:"deploy_target,omitempty"`
	OptionalDependsOn []string           `yaml:"optional_depends_on,omitempty" json:"optional_depends_on,omitempty"`
	AutoCommits       bool               `yaml:"auto_commits,omitempty" json:"auto_commits,omitempty"`

	// OnUpdate declares what the receiver does when this external slot is
	// recorded with a new version. When unset the receiver is record-only, the
	// historical default, and no deploy is run.
	OnUpdate *OnUpdateConfig `yaml:"on_update,omitempty" json:"on_update,omitempty"`
}

// OnUpdateConfig declares what the receiver does when this external slot is
// recorded with a new version. Absent => record-only (the historical default).
type OnUpdateConfig struct {
	// Deploy, when set, runs a scoped deploy in the same receiver run after the
	// slot is recorded. When nil the receiver remains record-only.
	Deploy *OnUpdateDeploy `yaml:"deploy,omitempty" json:"deploy,omitempty"`
}

// OnUpdateDeploy declares a scoped deploy run in the same receiver run after the
// slot is recorded, scoped to the updated component only.
type OnUpdateDeploy struct {
	// Workflow is the reusable-workflow path to run as the scoped deploy, mirroring
	// deploys[].workflow (local ".github/workflows/x.yaml" or "org/repo/.github/...@ref").
	Workflow string `yaml:"workflow" json:"workflow"`
}

// NotifyConfig defines how a satellite repo notifies its primary after dev deploys
type NotifyConfig struct {
	Repo     string `yaml:"repo" json:"repo"`                             // Primary repo to notify (e.g., "org/my-backend")
	Workflow string `yaml:"workflow,omitempty" json:"workflow,omitempty"` // Workflow to dispatch (default: "external-update.yaml")
	Token    string `yaml:"token,omitempty" json:"token,omitempty"`       // Secret name for cross-repo dispatch (default: "PRIMARY_REPO_TOKEN")
	// DeployName, when set, overrides the dispatched deploy_name with the name the
	// primary recognizes for this satellite. Use it when the satellite's local
	// build/deploy name differs from the external deploy name in the primary's
	// config. When empty the deploy_name is derived from the local deploys/builds.
	DeployName string `yaml:"deploy_name,omitempty" json:"deploy_name,omitempty"`
	// Environment, when set, overrides the dispatched environment with the
	// environment the primary recognizes for this satellite's external deploy. Use
	// it when the satellite's first local environment differs from (or is absent
	// for build-only satellites) the environment the primary expects. When empty
	// the environment is derived from the local environments (or defaults to dev).
	Environment string `yaml:"environment,omitempty" json:"environment,omitempty"`
}

// GetWorkflow returns the notify workflow name or default
func (n *NotifyConfig) GetWorkflow() string {
	if n.Workflow == "" {
		return "external-update.yaml"
	}
	return n.Workflow
}

// GetToken returns the token secret expression or default
func (n *NotifyConfig) GetToken() string {
	if n.Token == "" {
		return "${{ secrets.PRIMARY_REPO_TOKEN }}"
	}
	return normalizeTokenExpression(n.Token)
}

// ChangelogConfig defines changelog generation settings
type ChangelogConfig struct {
	Disabled     bool   `yaml:"disabled,omitempty" json:"disabled,omitempty"`         // true = disabled
	Workflow     string `yaml:"workflow,omitempty" json:"workflow,omitempty"`         // custom changelog workflow
	Contributors bool   `yaml:"contributors,omitempty" json:"contributors,omitempty"` // true = include contributors section
}

// ReleaseEnabled returns true if release management is enabled (default: true)
func (c *TrunkConfig) ReleaseEnabled() bool {
	return c.Release == nil || !c.Release.Disabled
}

// HasExternalRelease returns true if an external tool creates releases
func (c *TrunkConfig) HasExternalRelease() bool {
	return c.Release != nil && c.Release.Tag != ""
}

// HasCustomChangelog returns true if a custom changelog workflow is configured
func (c *TrunkConfig) HasCustomChangelog() bool {
	return c.Changelog != nil && c.Changelog.Workflow != ""
}

// ChangelogEnabled returns true if changelog generation is enabled (default: true)
func (c *TrunkConfig) ChangelogEnabled() bool {
	return c.Changelog == nil || !c.Changelog.Disabled
}

// HasReleaseArtifacts returns true if any build has artifacts configured
func (c *TrunkConfig) HasReleaseArtifacts() bool {
	for _, b := range c.Builds {
		if len(b.Artifacts) > 0 {
			return true
		}
	}
	return false
}

// GetAllReleaseArtifacts returns all artifact configs across all builds
// Each artifact name is prefixed with the build name for uniqueness
func (c *TrunkConfig) GetAllReleaseArtifacts() []struct {
	BuildName string
	Artifact  ArtifactConfig
} {
	var artifacts []struct {
		BuildName string
		Artifact  ArtifactConfig
	}
	for _, b := range c.Builds {
		for _, a := range b.Artifacts {
			artifacts = append(artifacts, struct {
				BuildName string
				Artifact  ArtifactConfig
			}{BuildName: b.Name, Artifact: a})
		}
	}
	return artifacts
}

// ParseResult is the output of parse-config command
type ParseResult struct {
	Config      TrunkConfig `json:"config"`
	BuildNames  []string    `json:"build_names"`
	DeployNames []string    `json:"deploy_names"`
	Valid       bool        `json:"valid"`
	Errors      []string    `json:"errors,omitempty"`
	Warnings    []string    `json:"warnings,omitempty"` // Non-fatal advisories (e.g. omitted/older schema_version)
}

// RunPolicy constants
const (
	RunPolicyDefault = "default"
	RunPolicyAlways  = "always"
	RunPolicyForce   = "force"
)

// OnFailure constants
const (
	OnFailureAbort    = "abort"
	OnFailureContinue = "continue"
)

// PromotionMode represents the promotion mode
type PromotionMode string

const (
	// ModeDefault promotes each environment to its immediate next only (sequential)
	// Supports force flag to continue on failure
	ModeDefault PromotionMode = "default"

	// ModeCascade promotes source through all intermediate environments to target (atomic)
	// Bails entirely on any failure - no partial state updates
	ModeCascade PromotionMode = "cascade"
)

// PromotionOption represents a cascade promotion target for manual mode
type PromotionOption struct {
	Name         string   `json:"name"`           // e.g., "dev-to-uat", "dev-to-prod"
	FromEnv      string   `json:"from_env"`       // source environment
	ToEnv        string   `json:"to_env"`         // target environment
	EnvsToUpdate []string `json:"envs_to_update"` // all envs between source and target (inclusive of target)
	IsPrerelease bool     `json:"is_prerelease"`  // triggers draft→prerelease (env before release marker)
	IsRelease    bool     `json:"is_release"`     // triggers prerelease→released (includes release marker)
	IncludesProd bool     `json:"includes_prod"`  // includes prod deployment
}

// GetPromotionModes returns the available promotion modes
func (c *TrunkConfig) GetPromotionModes() []PromotionMode {
	return []PromotionMode{ModeDefault, ModeCascade}
}

// GetAllDirectPromotionOptions returns all direct promotion options.
// Release states are determined by position, not by fake "release" environment:
//   - Promotion to second-from-top env (e.g., uat) = prerelease state
//   - Promotion to top env (e.g., prod) = released state
//
// For 4 envs [dev, test, uat, prod], generates:
//   - dev-to-test, dev-to-uat, test-to-uat (env-to-env, uat promotions trigger prerelease)
//   - dev-to-prod, test-to-prod, uat-to-prod (includes prod deployment, triggers released)
func (c *TrunkConfig) GetAllDirectPromotionOptions() []PromotionOption {
	envs := c.Environments
	if len(envs) < 2 {
		return nil
	}

	var options []PromotionOption
	prodEnv := envs[len(envs)-1]
	prereleaseEnv := envs[len(envs)-2] // Second from top = prerelease environment

	// Generate from each environment (except prod)
	for i := 0; i < len(envs)-1; i++ {
		fromEnv := envs[i]

		// To each subsequent environment (including prod)
		for j := i + 1; j < len(envs); j++ {
			toEnv := envs[j]
			name := fromEnv + "-to-" + toEnv

			// Calculate all environments that will be updated (from+1 to target, inclusive)
			envsToUpdate := make([]string, 0, j-i)
			for k := i + 1; k <= j; k++ {
				envsToUpdate = append(envsToUpdate, envs[k])
			}

			isProd := toEnv == prodEnv
			isPrerelease := toEnv == prereleaseEnv

			options = append(options, PromotionOption{
				Name:         name,
				FromEnv:      fromEnv,
				ToEnv:        toEnv,
				EnvsToUpdate: envsToUpdate,
				IsRelease:    isProd, // Promoting to prod triggers release
				IncludesProd: isProd,
				IsPrerelease: isPrerelease, // Promoting to prerelease env triggers prerelease
			})
		}
	}

	return options
}

// GetCascadeTargets returns all valid cascade promotion targets for manual mode
// These are the direct promotion options (e.g., dev-to-test, dev-to-prod)
func (c *TrunkConfig) GetCascadeTargets() []PromotionOption {
	return c.GetAllDirectPromotionOptions()
}

// IsSingleEnvironment returns true if only one environment is configured
// Single-environment projects get a Release workflow instead of Promote
func (c *TrunkConfig) IsSingleEnvironment() bool {
	return len(c.Environments) == 1
}

// IsFirstEnvironment returns true if env is the first (build target)
func (c *TrunkConfig) IsFirstEnvironment(env string) bool {
	if len(c.Environments) == 0 {
		return false
	}
	return c.Environments[0] == env
}

// IsLastEnvironment returns true if env is the last (production)
func (c *TrunkConfig) IsLastEnvironment(env string) bool {
	if len(c.Environments) == 0 {
		return false
	}
	return c.Environments[len(c.Environments)-1] == env
}

// GetEnvironmentIndex returns the index of the environment, -1 if not found
func (c *TrunkConfig) GetEnvironmentIndex(env string) int {
	for i, e := range c.Environments {
		if e == env {
			return i
		}
	}
	return -1
}

// GetNextEnvironment returns the next environment in the list, empty if last
func (c *TrunkConfig) GetNextEnvironment(env string) string {
	idx := c.GetEnvironmentIndex(env)
	if idx == -1 || idx == len(c.Environments)-1 {
		return ""
	}
	return c.Environments[idx+1]
}

// GetEnvironmentsInRange returns all environments from start to end (inclusive)
// Returns nil if start or end not found, or if end comes before start
func (c *TrunkConfig) GetEnvironmentsInRange(start, end string) []string {
	startIdx := c.GetEnvironmentIndex(start)
	endIdx := c.GetEnvironmentIndex(end)

	if startIdx == -1 || endIdx == -1 || endIdx < startIdx {
		return nil
	}

	result := make([]string, 0, endIdx-startIdx+1)
	for i := startIdx; i <= endIdx; i++ {
		result = append(result, c.Environments[i])
	}
	return result
}

// Callback type constants
const (
	CallbackTypeBuild    = "build"
	CallbackTypeDeploy   = "deploy"
	CallbackTypeValidate = "validate"
	CallbackTypeExternal = "external" // External repo deploy
)

// JobID returns the prefixed job ID for a callback (e.g., "build-app", "deploy-app")
func JobID(callbackType, name string) string {
	if callbackType == CallbackTypeValidate {
		return "validate"
	}
	return callbackType + "-" + name
}

// OutputKey converts a job ID to an output-safe key by using underscores instead of hyphens
// GitHub Actions expressions interpret hyphens as subtraction, so we use underscores for output keys
// Example: "build-app" -> "build_app"
func OutputKey(jobID string) string {
	return strings.ReplaceAll(jobID, "-", "_")
}

// DisplayName returns the display name for a callback (e.g., "Build (app)", "Deploy (app)")
func DisplayName(callbackType, name string) string {
	if callbackType == CallbackTypeValidate {
		return "Validate (validate)"
	}
	switch callbackType {
	case CallbackTypeBuild:
		return "Build (" + name + ")"
	case CallbackTypeDeploy:
		return "Deploy (" + name + ")"
	default:
		return name
	}
}

// ResolveDependency resolves a dependency reference to its job ID.
// Supports:
//   - Explicit: "build:app" -> "build-app", "deploy:infra" -> "deploy-infra"
//   - Implicit: "app" -> infers type based on context (deploys look in builds first)
//
// Returns the resolved job ID and an error if ambiguous or not found.
func (c *TrunkConfig) ResolveDependency(depRef string, fromType string) (string, error) {
	// Check for explicit prefix
	if idx := indexByte(depRef, ':'); idx != -1 {
		prefix := depRef[:idx]
		name := depRef[idx+1:]
		switch prefix {
		case "build":
			for _, b := range c.Builds {
				if b.Name == name {
					return JobID(CallbackTypeBuild, name), nil
				}
			}
			return "", fmt.Errorf("build '%s' not found", name)
		case "deploy":
			for _, d := range c.Deploys {
				if d.Name == name {
					return JobID(CallbackTypeDeploy, name), nil
				}
			}
			return "", fmt.Errorf("deploy '%s' not found", name)
		case "external":
			for _, ext := range c.External {
				for _, d := range ext.Deploys {
					if d.Name == name {
						return JobID(CallbackTypeExternal, name), nil
					}
				}
			}
			return "", fmt.Errorf("external deploy '%s' not found", name)
		case "validate":
			if c.Validate != nil {
				return "validate", nil
			}
			return "", fmt.Errorf("validate not configured")
		default:
			return "", fmt.Errorf("unknown dependency type: %s", prefix)
		}
	}

	// Implicit resolution - check based on fromType
	var foundInBuilds, foundInDeploys, foundInExternal bool
	for _, b := range c.Builds {
		if b.Name == depRef {
			foundInBuilds = true
			break
		}
	}
	for _, d := range c.Deploys {
		if d.Name == depRef {
			foundInDeploys = true
			break
		}
	}
	// Check external deploys
	for _, ext := range c.External {
		for _, d := range ext.Deploys {
			if d.Name == depRef {
				foundInExternal = true
				break
			}
		}
		if foundInExternal {
			break
		}
	}

	// Smart inference based on context
	switch fromType {
	case CallbackTypeDeploy:
		// Deploys prefer builds first (most common case)
		if foundInBuilds {
			return JobID(CallbackTypeBuild, depRef), nil
		}
		if foundInDeploys {
			return JobID(CallbackTypeDeploy, depRef), nil
		}
		// External deploys use "external-" prefix
		if foundInExternal {
			return JobID(CallbackTypeExternal, depRef), nil
		}
	case CallbackTypeBuild:
		// Builds can only depend on other builds or validate
		if foundInBuilds {
			return JobID(CallbackTypeBuild, depRef), nil
		}
	}

	// Count how many places we found it
	foundCount := 0
	if foundInBuilds {
		foundCount++
	}
	if foundInDeploys {
		foundCount++
	}
	if foundInExternal {
		foundCount++
	}

	// Ambiguous case - multiple exist
	if foundCount > 1 {
		var locations []string
		if foundInBuilds {
			locations = append(locations, "build")
		}
		if foundInDeploys {
			locations = append(locations, "deploy")
		}
		if foundInExternal {
			locations = append(locations, "external")
		}
		return "", fmt.Errorf("ambiguous dependency '%s': exists as %s. Use explicit prefix (e.g., 'build:%s' or 'deploy:%s')", depRef, strings.Join(locations, ", "), depRef, depRef)
	}

	// Not found
	if foundCount == 0 {
		return "", fmt.Errorf("dependency '%s' not found in builds, deploys, or external", depRef)
	}

	// Single match found
	if foundInBuilds {
		return JobID(CallbackTypeBuild, depRef), nil
	}
	if foundInDeploys {
		return JobID(CallbackTypeDeploy, depRef), nil
	}
	return JobID(CallbackTypeExternal, depRef), nil
}

// ComponentConfig is the reserved per-component descriptor. Only the addressing
// shape is frozen in v1; richer per-component config lands post-1.0 additively.
type ComponentConfig struct {
	// Path is the subtree this component owns within the repo (reserved).
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	// TagPrefix is the per-component version tag prefix (reserved). Empty means
	// inherit the manifest-level tag_prefix.
	TagPrefix string `yaml:"tag_prefix,omitempty" json:"tag_prefix,omitempty"`
}

// ComponentState is the reserved per-component recorded-state entry.
type ComponentState struct {
	Version     string `yaml:"version,omitempty" json:"version,omitempty"`
	SHA         string `yaml:"sha,omitempty" json:"sha,omitempty"`
	CommittedAt string `yaml:"committed_at,omitempty" json:"committed_at,omitempty"`
	CommittedBy string `yaml:"committed_by,omitempty" json:"committed_by,omitempty"`
}

// ComponentReleaseState is the reserved per-component published-release entry.
type ComponentReleaseState struct {
	Version    string `yaml:"version,omitempty" json:"version,omitempty"`
	SHA        string `yaml:"sha,omitempty" json:"sha,omitempty"`
	ReleasedOn string `yaml:"released_on,omitempty" json:"released_on,omitempty"`
}

// indexByte returns the index of the first instance of c in s, or -1 if c is not present
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// GetAllTriggers returns trigger patterns for the orchestrate workflow paths filter.
// Priority order:
//  1. Global triggers (config.triggers) - if defined, use exclusively
//  2. Component triggers - collect from validate, builds, and deploys
//
// If no triggers are defined anywhere, returns nil (orchestrate runs on all pushes).
func (c *TrunkConfig) GetAllTriggers() []string {
	// If global triggers are defined, use them exclusively
	if len(c.Triggers) > 0 {
		triggers := make([]string, len(c.Triggers))
		copy(triggers, c.Triggers)
		sort.Strings(triggers)
		return triggers
	}

	// Otherwise, collect from all components
	seen := make(map[string]bool)
	var triggers []string

	// Add validate triggers
	if c.Validate != nil {
		for _, t := range c.Validate.Triggers {
			if !seen[t] {
				seen[t] = true
				triggers = append(triggers, t)
			}
		}
	}

	// Add build triggers
	for _, b := range c.Builds {
		for _, t := range b.Triggers {
			if !seen[t] {
				seen[t] = true
				triggers = append(triggers, t)
			}
		}
	}

	// Add deploy triggers
	for _, d := range c.Deploys {
		for _, t := range d.Triggers {
			if !seen[t] {
				seen[t] = true
				triggers = append(triggers, t)
			}
		}
	}

	// Sort for deterministic output
	sort.Strings(triggers)
	return triggers
}

// IsPrimary returns true if this repo coordinates external repos
func (c *TrunkConfig) IsPrimary() bool {
	return len(c.External) > 0
}

// IsSatellite returns true if this repo notifies a primary after dev deploys
func (c *TrunkConfig) IsSatellite() bool {
	return c.Notify != nil && c.Notify.Repo != ""
}

// GetExternalDeploy returns an external deploy config by name, or nil if not found
func (c *TrunkConfig) GetExternalDeploy(name string) (*ExternalDeployConfig, *ExternalRepoConfig) {
	for i := range c.External {
		for j := range c.External[i].Deploys {
			if c.External[i].Deploys[j].Name == name {
				return &c.External[i].Deploys[j], &c.External[i]
			}
		}
	}
	return nil, nil
}

// GetAllExternalDeploys returns all external deploys across all external repos
func (c *TrunkConfig) GetAllExternalDeploys() []struct {
	Repo   *ExternalRepoConfig
	Deploy *ExternalDeployConfig
} {
	var result []struct {
		Repo   *ExternalRepoConfig
		Deploy *ExternalDeployConfig
	}
	for i := range c.External {
		for j := range c.External[i].Deploys {
			result = append(result, struct {
				Repo   *ExternalRepoConfig
				Deploy *ExternalDeployConfig
			}{
				Repo:   &c.External[i],
				Deploy: &c.External[i].Deploys[j],
			})
		}
	}
	return result
}

// GetAllExternalDeployNames returns the names of all external deploys
func (c *TrunkConfig) GetAllExternalDeployNames() []string {
	var names []string
	for _, ext := range c.External {
		for _, d := range ext.Deploys {
			names = append(names, d.Name)
		}
	}
	return names
}

// IsExternalWorkflow returns true if the workflow path references an external repo
// External paths have format: "org/repo/.github/workflows/file.yaml@ref"
// Local paths start with "." or ".github/"
func IsExternalWorkflow(workflowPath string) bool {
	return !strings.HasPrefix(workflowPath, ".") && strings.Contains(workflowPath, "/")
}

// GetExternalRef returns the ref for an external repo config, defaulting to trunk_branch
func (e *ExternalRepoConfig) GetRef(trunkBranch string) string {
	if e.Ref == "" {
		return trunkBranch
	}
	return e.Ref
}

// GetTriggersForDeploy returns the trigger patterns for a deploy.
// If deploy has depends_on, returns the first referenced build's triggers.
// If deploy has own triggers, returns those.
// Otherwise returns nil (deploy always runs).
func (c *TrunkConfig) GetTriggersForDeploy(deployName string) []string {
	// Find the deploy
	var deploy *DeployConfig
	for i := range c.Deploys {
		if c.Deploys[i].Name == deployName {
			deploy = &c.Deploys[i]
			break
		}
	}
	if deploy == nil {
		return nil
	}

	// If depends_on build, use build's triggers
	if len(deploy.DependsOn) > 0 {
		for _, b := range c.Builds {
			if b.Name == deploy.DependsOn[0] {
				return b.Triggers
			}
		}
	}

	// Otherwise use deploy's own triggers
	if len(deploy.Triggers) > 0 {
		return deploy.Triggers
	}

	return nil
}
