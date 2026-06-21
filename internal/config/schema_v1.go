package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// This file holds the v1 manifest schema field shapes that are designed and
// frozen now but whose generation behavior lands in later, additive work. The
// struct shapes (YAML keys, types, nesting, defaults) are part of the frozen v1
// contract: nothing here may require a breaking change to wire up later. Parsing
// and structural validation are implemented; emit/generation behavior is not.

// SecretsConfig models the per-callback secrets passing union. It is either the
// opt-in "inherit" form (the scalar "inherit", or the mapping {inherit: true})
// or an explicit map of called-workflow secret name to caller secret name (the
// least-privilege form). An unset secrets field (nil SecretsConfig) emits no
// secrets block at all.
type SecretsConfig struct {
	// Inherit is true when the manifest opted in to inheriting all caller secrets,
	// via the scalar "inherit" or the mapping {inherit: true}.
	Inherit bool `json:"inherit,omitempty"`
	// Map holds the explicit mapping (called name -> caller name) when a mapping of
	// secret names was provided. Nil when Inherit is true.
	Map map[string]string `json:"map,omitempty"`
}

// UnmarshalYAML accepts the scalar "inherit", the mapping {inherit: <bool>}, or a
// mapping of secret names. inherit may not be mixed with explicit secret keys.
func (s *SecretsConfig) UnmarshalYAML(value *yaml.Node) error {
	// A bare `secrets:` (null node) is treated as unset.
	if value.Tag == "!!null" {
		return nil
	}
	switch value.Kind {
	case yaml.ScalarNode:
		var str string
		if err := value.Decode(&str); err != nil {
			return err
		}
		if str != "inherit" {
			return fmt.Errorf("secrets: the only valid scalar form is \"inherit\"; got %q (use a mapping for explicit secrets)", str)
		}
		s.Inherit = true
		return nil
	case yaml.MappingNode:
		// Reject mixing the inherit key with explicit secret mappings.
		hasInherit := false
		for i := 0; i+1 < len(value.Content); i += 2 {
			if value.Content[i].Value == "inherit" {
				hasInherit = true
				break
			}
		}
		if hasInherit {
			if len(value.Content) != 2 {
				return fmt.Errorf("secrets: cannot mix \"inherit\" with explicit secret mappings")
			}
			var b bool
			if err := value.Content[1].Decode(&b); err != nil {
				return fmt.Errorf("secrets: inherit value must be a boolean: %w", err)
			}
			s.Inherit = b
			return nil
		}
		m := map[string]string{}
		if err := value.Decode(&m); err != nil {
			return err
		}
		s.Map = m
		return nil
	default:
		return fmt.Errorf("secrets: expected \"inherit\" or a mapping of secret names")
	}
}

// MarshalYAML emits the scalar "inherit" or the explicit mapping.
func (s SecretsConfig) MarshalYAML() (interface{}, error) {
	if s.Inherit {
		return "inherit", nil
	}
	return s.Map, nil
}

// RunsOn models the runs_on union: a scalar label (form A), a list of labels
// (form B), or a {group, labels} object (form C). It mirrors the full GHA
// runs-on grammar so self-hosted / runner-group adoption needs no reshape.
type RunsOn struct {
	// Label holds a single scalar label (form A) such as "ubuntu-latest".
	Label string `json:"label,omitempty"`
	// Labels holds a list of labels (form B) such as [self-hosted, macOS, arm64].
	Labels []string `json:"labels,omitempty"`
	// Group holds the runner group name (form C).
	Group string `json:"group,omitempty"`
}

// UnmarshalYAML accepts a scalar label, a sequence of labels, or a {group,
// labels} mapping.
func (r *RunsOn) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		return value.Decode(&r.Label)
	case yaml.SequenceNode:
		return value.Decode(&r.Labels)
	case yaml.MappingNode:
		var obj struct {
			Group  string   `yaml:"group"`
			Labels []string `yaml:"labels"`
		}
		if err := value.Decode(&obj); err != nil {
			return err
		}
		if obj.Group == "" && len(obj.Labels) == 0 {
			return fmt.Errorf("runs_on: object form requires at least one of group or labels")
		}
		r.Group = obj.Group
		r.Labels = obj.Labels
		return nil
	default:
		return fmt.Errorf("runs_on: expected a label, a list of labels, or a {group, labels} object")
	}
}

// MarshalYAML emits the most compact valid form for the populated fields.
func (r RunsOn) MarshalYAML() (interface{}, error) {
	if r.Group != "" {
		return map[string]interface{}{"group": r.Group, "labels": r.Labels}, nil
	}
	if len(r.Labels) > 0 {
		return r.Labels, nil
	}
	return r.Label, nil
}

// MatrixConfig models build fan-out (matrix:) on a build callback. Builds only.
type MatrixConfig struct {
	// Dimensions are the cross-product axes (e.g. os: [...], arch: [...]).
	Dimensions map[string][]string `yaml:"dimensions,omitempty" json:"dimensions,omitempty"`
	// MaxParallel caps concurrent matrix legs (0 = GHA default).
	MaxParallel int `yaml:"max_parallel,omitempty" json:"max_parallel,omitempty"`
	// FailFast is a pointer so "unset" differs from "false". When nil the
	// generator applies its default (GHA's true for matrix builds).
	FailFast *bool `yaml:"fail_fast,omitempty" json:"fail_fast,omitempty"`
}

// RolloutConfig models deploy rollout strategy (rollout:) on a deploy callback.
// Deploys only. There is no shared strategy: block; matrix: and rollout: are
// the canonical, separate concerns.
type RolloutConfig struct {
	// Type is one of default|rolling|canary|blue_green (default: default).
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
	// MaxParallel caps concurrent rollout waves (e.g. region-by-region).
	MaxParallel int `yaml:"max_parallel,omitempty" json:"max_parallel,omitempty"`
	// FailFast is a pointer so "unset" differs from "false".
	FailFast *bool `yaml:"fail_fast,omitempty" json:"fail_fast,omitempty"`
	// Canary is the reserved canary sub-block, used when type == canary.
	Canary *CanaryConfig `yaml:"canary,omitempty" json:"canary,omitempty"`
	// BlueGreen is the reserved blue/green sub-block, used when type == blue_green.
	BlueGreen *BlueGreenConfig `yaml:"blue_green,omitempty" json:"blue_green,omitempty"`
}

// Rollout type constants.
const (
	RolloutTypeDefault   = "default"
	RolloutTypeRolling   = "rolling"
	RolloutTypeCanary    = "canary"
	RolloutTypeBlueGreen = "blue_green"
)

// CanaryConfig is the reserved canary rollout sub-block.
type CanaryConfig struct {
	// Steps are the percent waves (for example [10, 50, 100]).
	Steps []int `yaml:"steps,omitempty" json:"steps,omitempty"`
	// Analysis is a workflow path that gates each wave.
	Analysis string `yaml:"analysis,omitempty" json:"analysis,omitempty"`
	// Percent is the single initial canary weight (1..100). RESERVED - not yet wired to generation.
	Percent int `yaml:"percent,omitempty" json:"percent,omitempty"`
	// BakeTime is the soak duration after traffic shift before promotion (Go duration string, e.g. "30m"). RESERVED - not yet wired to generation.
	BakeTime string `yaml:"bake_time,omitempty" json:"bake_time,omitempty"`
	// PromoteCallback is the local callback workflow path invoked on promotion. RESERVED - not yet wired to generation.
	PromoteCallback string `yaml:"promote_callback,omitempty" json:"promote_callback,omitempty"`
	// RollbackCallback is the local callback workflow path invoked on rollback. RESERVED - not yet wired to generation.
	RollbackCallback string `yaml:"rollback_callback,omitempty" json:"rollback_callback,omitempty"`
}

// BlueGreenConfig is the reserved blue/green rollout sub-block.
type BlueGreenConfig struct {
	// Switch is a workflow path that performs the blue/green cutover.
	Switch string `yaml:"switch,omitempty" json:"switch,omitempty"`
}

// DispatchInput models a single operator-facing workflow_dispatch input.
type DispatchInput struct {
	// Type is one of string|boolean|choice|environment|number.
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
	// Options enumerates the valid values for a choice input.
	Options []string `yaml:"options,omitempty" json:"options,omitempty"`
	// Default is the default value (any of the supported types).
	Default interface{} `yaml:"default,omitempty" json:"default,omitempty"`
	// Description is the operator-facing help text.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Required marks the input as required.
	Required bool `yaml:"required,omitempty" json:"required,omitempty"`
}

// Dispatch input type constants (GHA dispatch input types).
const (
	DispatchInputTypeString      = "string"
	DispatchInputTypeBoolean     = "boolean"
	DispatchInputTypeChoice      = "choice"
	DispatchInputTypeEnvironment = "environment"
	DispatchInputTypeNumber      = "number"
)

// ExtraTriggers models config-level non-push trigger types.
type ExtraTriggers struct {
	// Schedule holds cron schedule entries.
	Schedule []ScheduleEntry `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	// RepositoryDispatch enables the repository_dispatch trigger.
	RepositoryDispatch *RepositoryDispatchTrigger `yaml:"repository_dispatch,omitempty" json:"repository_dispatch,omitempty"`
	// WorkflowRun enables the workflow_run trigger.
	WorkflowRun *WorkflowRunTrigger `yaml:"workflow_run,omitempty" json:"workflow_run,omitempty"`
	// MergeGroup, when present (even empty), wires the GHA merge-queue trigger.
	// The validation lane behavior lives in the separate merge_queue: block.
	MergeGroup *MergeGroupTrigger `yaml:"merge_group,omitempty" json:"merge_group,omitempty"`
}

// ScheduleEntry is a single cron schedule.
type ScheduleEntry struct {
	Cron string `yaml:"cron" json:"cron"`
}

// RepositoryDispatchTrigger configures the repository_dispatch trigger.
type RepositoryDispatchTrigger struct {
	Types []string `yaml:"types,omitempty" json:"types,omitempty"`
}

// WorkflowRunTrigger configures the workflow_run trigger.
type WorkflowRunTrigger struct {
	Workflows []string `yaml:"workflows,omitempty" json:"workflows,omitempty"`
	Types     []string `yaml:"types,omitempty" json:"types,omitempty"`
}

// MergeGroupTrigger wires the raw merge_group trigger. Presence enables it; the
// struct is intentionally empty for v1 (additive fields may arrive later).
type MergeGroupTrigger struct{}

// PRPreviewConfig is the opt-in read-only PR plan-preview lane (#40).
type PRPreviewConfig struct {
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Comment bool `yaml:"comment,omitempty" json:"comment,omitempty"`
}

// ValidateCheckConfig is the opt-in manifest-validation-as-a-PR-check lane (#41).
type ValidateCheckConfig struct {
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// MergeQueueConfig is the opt-in merge-queue validation lane (#42). The raw
// trigger lives separately under extra_triggers.merge_group.
type MergeQueueConfig struct {
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// DriftCheckConfig is the opt-in workflow drift-check lane (#229). When Enabled,
// cascade emits a pull_request workflow that runs cascade verify and fails the
// check on drift. When Comment is also set, cascade emits the fork-safe
// workflow_run companion that posts the verify result as a sticky PR comment.
//
// The companion derives the target PR number ONLY from trusted workflow_run run
// metadata, never from the artifact the pull_request job uploads, so a fork PR
// cannot redirect the comment at another PR.
type DriftCheckConfig struct {
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Comment bool `yaml:"comment,omitempty" json:"comment,omitempty"`
}

// Pin mode constants.
const (
	PinModeTag = "tag"
	PinModeSHA = "sha"
)

// TelemetryConfig is the reserved vendor-neutral metrics seam.
type TelemetryConfig struct {
	Enabled bool   `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Adapter string `yaml:"adapter,omitempty" json:"adapter,omitempty"` // none | datadog | <future>
	// Webhook is the reserved generic vendor-neutral JSON-POST sink. RESERVED -
	// not yet wired to generation/emit. Carries the destination shape behind the
	// adapter seam so no vendor client is baked into core.
	Webhook *TelemetryWebhook `yaml:"webhook,omitempty" json:"webhook,omitempty"`
	// JobSummary toggles the run-UI summary table. A pointer so unset differs
	// from an explicit false (mirroring the FailFast "unset != false" precedent).
	// RESERVED - not yet wired to generation/emit; default-on behavior lands in a
	// later minor.
	JobSummary *bool `yaml:"job_summary,omitempty" json:"job_summary,omitempty"`
}

// Telemetry adapter constants. The vendor stays an Adapter value behind the
// seam; no vendor client is wired into core. These name the known adapter
// values for documentation and future use and are not enforced as an enum (an
// arbitrary adapter string parses today and must keep parsing).
const (
	TelemetryAdapterNone    = "none"
	TelemetryAdapterDatadog = "datadog"
)

// TelemetryWebhook is the reserved generic JSON-POST telemetry sink. RESERVED -
// not yet wired to generation/emit.
type TelemetryWebhook struct {
	// URL is the destination the run posts telemetry to.
	URL string `yaml:"url,omitempty" json:"url,omitempty"`
	// SecretName is the name of a GitHub Actions secret holding the auth token.
	// This is a secret REFERENCE, never an inline token value: the resolver
	// reads the named secret at run time rather than carrying a raw credential
	// in the manifest. RESERVED - not yet wired to generation/emit.
	SecretName string `yaml:"secret_name,omitempty" json:"secret_name,omitempty"`
}

// EnvironmentConfig is the reserved per-environment settings block, keyed by env
// name under config.environment_config. environments stays the ordered source
// of truth for env names; this block carries per-env settings without fanning
// names into multiple list fields.
type EnvironmentConfig struct {
	// GHAEnvironment maps this env to a GitHub Environment (deployment records,
	// required reviewers, wait timers, env-scoped secrets).
	GHAEnvironment string `yaml:"gha_environment,omitempty" json:"gha_environment,omitempty"`
}

// DeployTarget is the reserved GitOps-mirror deploy variant. It complements,
// not replaces, the External/Notify cross-repo dispatch model.
type DeployTarget struct {
	// Mode is gitops | dispatch (default: dispatch via external/notify).
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
	// Repo is the GitOps config repo (e.g. org/gitops-config).
	Repo string `yaml:"repo,omitempty" json:"repo,omitempty"`
	// Path is the file to mutate in the target repo.
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	// Field is the field to bump (e.g. image.tag).
	Field string `yaml:"field,omitempty" json:"field,omitempty"`
	// Value is the value to write (may be a GHA expression).
	Value string `yaml:"value,omitempty" json:"value,omitempty"`
	// Branch is the target branch for the GitOps write (env-to-branch
	// mapping); default is the target repo's default branch.
	// RESERVED - not yet wired to generation; meaningful only when mode is gitops.
	Branch string `yaml:"branch,omitempty" json:"branch,omitempty"`
	// TrackSHA, when true, records the post-push HEAD SHA of the target repo
	// into state. RESERVED - not yet wired to generation; meaningful only when
	// mode is gitops.
	TrackSHA bool `yaml:"track_sha,omitempty" json:"track_sha,omitempty"`
}

// Deploy target mode constants.
const (
	DeployTargetModeDispatch = "dispatch"
	DeployTargetModeGitOps   = "gitops"
)

// GetPinMode returns the configured pin mode or the default ("tag").
func (c *TrunkConfig) GetPinMode() string {
	if c == nil || c.PinMode == "" {
		return PinModeTag
	}
	return c.PinMode
}

// GetType returns the rollout type or the default ("default").
func (r *RolloutConfig) GetType() string {
	if r == nil || r.Type == "" {
		return RolloutTypeDefault
	}
	return r.Type
}

// GetMode returns the deploy-target mode or the default ("dispatch").
func (d *DeployTarget) GetMode() string {
	if d == nil || d.Mode == "" {
		return DeployTargetModeDispatch
	}
	return d.Mode
}
