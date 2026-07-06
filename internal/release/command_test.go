package release

import (
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
