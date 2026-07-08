package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewCommand_TagOnlyFlag asserts the manage-release command exposes the
// --tag-only flag (defaulting off) that the generated composite action forwards
// so orchestrate's tag-only rc cut reaches release.go without creating a draft.
func TestNewCommand_TagOnlyFlag(t *testing.T) {
	cmd := NewCommand()
	flag := cmd.Flags().Lookup("tag-only")
	if flag == nil {
		t.Fatal("expected --tag-only flag to be registered on manage-release")
	}
	if flag.DefValue != "false" {
		t.Errorf("--tag-only default = %q, want false", flag.DefValue)
	}
}

// TestValidateManageReleaseFlags_SHARequiredOnlyForTagCreatingActions asserts
// that --sha is required only for the tag-creating actions (create, prerelease,
// publish) and is optional for the tag-addressed actions (lock, update, delete),
// which resolve the release by tag and treat SHA as an optional disambiguator.
func TestValidateManageReleaseFlags_SHARequiredOnlyForTagCreatingActions(t *testing.T) {
	const (
		repo = "owner/repo"
		env  = "staging"
		tag  = "v1.0.0-rc.1"
	)

	tests := []struct {
		name      string
		action    Action
		sha       string
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "lock without sha passes",
			action:  ActionLock,
			sha:     "",
			wantErr: false,
		},
		{
			name:    "update without sha passes",
			action:  ActionUpdate,
			sha:     "",
			wantErr: false,
		},
		{
			name:    "delete without sha passes",
			action:  ActionDelete,
			sha:     "",
			wantErr: false,
		},
		{
			name:      "create without sha fails",
			action:    ActionCreate,
			sha:       "",
			wantErr:   true,
			errSubstr: "--sha is required",
		},
		{
			name:      "prerelease without sha fails",
			action:    ActionPrerelease,
			sha:       "",
			wantErr:   true,
			errSubstr: "--sha is required",
		},
		{
			name:      "publish without sha fails",
			action:    ActionPublish,
			sha:       "",
			wantErr:   true,
			errSubstr: "--sha is required",
		},
		{
			name:    "create with sha passes",
			action:  ActionCreate,
			sha:     "abc123",
			wantErr: false,
		},
		{
			name:    "lock with sha passes",
			action:  ActionLock,
			sha:     "abc123",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateManageReleaseFlags(tt.action, repo, env, tt.sha, tag)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for action %q with sha=%q, got nil", tt.action, tt.sha)
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Fatalf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error for action %q with sha=%q, got %q", tt.action, tt.sha, err.Error())
			}
		})
	}
}

// TestValidateManageReleaseFlags_OtherRequiredFields confirms repo, environment,
// and tag remain required for every action regardless of the SHA gating.
func TestValidateManageReleaseFlags_OtherRequiredFields(t *testing.T) {
	tests := []struct {
		name      string
		repo      string
		env       string
		tag       string
		errSubstr string
	}{
		{name: "missing repo", repo: "", env: "staging", tag: "v1", errSubstr: "--repo is required"},
		{name: "missing environment", repo: "owner/repo", env: "", tag: "v1", errSubstr: "--environment is required"},
		{name: "missing tag", repo: "owner/repo", env: "staging", tag: "", errSubstr: "--tag is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use ActionLock (sha-optional) so the failing field is the one under test.
			err := validateManageReleaseFlags(ActionLock, tt.repo, tt.env, "", tt.tag)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.errSubstr) {
				t.Fatalf("expected error containing %q, got %q", tt.errSubstr, err.Error())
			}
		})
	}
}

// TestComponentReapOptions_NoComponentIsSingleComponentPath asserts that without
// a declared component the reaper keeps its single-component behavior: no options
// are threaded, so the resulting Manager carries no grammar (nil) and reaps
// exactly as the historical permissive path did.
func TestComponentReapOptions_NoComponentIsSingleComponentPath(t *testing.T) {
	opts, err := componentReapOptions("", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts != nil {
		t.Fatalf("expected no options for the single-component path, got %d", len(opts))
	}

	mgr := NewManager("owner/repo", "tok", opts...)
	if mgr.grammar != nil {
		t.Fatalf("expected nil grammar (legacy path), got %+v", *mgr.grammar)
	}
}

// TestComponentReapOptions_ComponentRequiresConfig asserts a named component
// without --config is a loud configuration error rather than silently falling
// back to permissive reaping.
func TestComponentReapOptions_ComponentRequiresConfig(t *testing.T) {
	_, err := componentReapOptions("", "ci", "api")
	if err == nil {
		t.Fatal("expected an error when --component is set without --config")
	}
	if !strings.Contains(err.Error(), "--config is required") {
		t.Fatalf("expected a --config-required error, got %q", err.Error())
	}
}

// TestComponentReapOptions_ThreadsStrictComponentGrammar asserts that a declared
// component's resolved grammar reaches the Manager: the option threads a strict
// grammar carrying the component's prefix and custom pre-release token, so the
// reaper is scoped to that component's namespace.
func TestComponentReapOptions_ThreadsStrictComponentGrammar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
    components:
      api:
        path: services/api
        tag_prefix: api-
        tag_grammar:
          prerelease_token: beta
`
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}

	opts, err := componentReapOptions(path, "ci", "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts) != 1 {
		t.Fatalf("expected exactly one option, got %d", len(opts))
	}

	mgr := NewManager("owner/repo", "tok", opts...)
	if mgr.grammar == nil {
		t.Fatal("expected a threaded component grammar, got nil")
	}
	if got := mgr.grammar.Prefix; got != "api-" {
		t.Errorf("grammar prefix = %q, want api-", got)
	}
	if got := mgr.grammar.PreReleaseToken; got != "beta" {
		t.Errorf("grammar pre-release token = %q, want beta", got)
	}
	if !mgr.grammar.StrictPrefix {
		t.Error("expected StrictPrefix to be forced on for a declared component")
	}
}
