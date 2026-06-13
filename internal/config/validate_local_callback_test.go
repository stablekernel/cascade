package config

import (
	"strings"
	"testing"
)

func TestValidateLocalCallbackWorkflowPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		prefix   string
		workflow string
		wantErrs int
		wantMsg  string
	}{
		{
			name:     "empty workflow accepted",
			prefix:   "builds[0]",
			workflow: "",
			wantErrs: 0,
		},
		{
			name:     "bare filename accepted",
			prefix:   "builds[0]",
			workflow: "build.yaml",
			wantErrs: 0,
		},
		{
			name:     "github workflows path accepted",
			prefix:   "builds[0]",
			workflow: ".github/workflows/x.yaml",
			wantErrs: 0,
		},
		{
			name:     "dot-slash github workflows path accepted",
			prefix:   "builds[0]",
			workflow: "./.github/workflows/x.yaml",
			wantErrs: 0,
		},
		{
			name:     "cross-repo external ref accepted",
			prefix:   "builds[0]",
			workflow: "owner/repo/.github/workflows/x.yml@ref",
			wantErrs: 0,
		},
		{
			name:     "local subdir path rejected",
			prefix:   "deploys[0]",
			workflow: "ci/build.yaml",
			wantErrs: 1,
			wantMsg:  `deploys[0]: local callback workflow must be a .github/workflows/... path or a bare filename, got "ci/build.yaml"`,
		},
		{
			name:     "dot-slash local path rejected",
			prefix:   "deploys[1]",
			workflow: "./build.yaml",
			wantErrs: 1,
			wantMsg:  `deploys[1]: local callback workflow must be a .github/workflows/... path or a bare filename, got "./build.yaml"`,
		},
		{
			name:     "github non-workflows subdir rejected",
			prefix:   "builds[1]",
			workflow: ".github/foo/x.yaml",
			wantErrs: 1,
			wantMsg:  `builds[1]: local callback workflow must be a .github/workflows/... path or a bare filename, got ".github/foo/x.yaml"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errs := validateLocalCallbackWorkflowPath(tc.prefix, tc.workflow)
			if len(errs) != tc.wantErrs {
				t.Errorf("got %d errors, want %d: %v", len(errs), tc.wantErrs, errs)
				return
			}
			if tc.wantMsg != "" {
				if len(errs) == 0 || !strings.Contains(errs[0], tc.wantMsg) {
					t.Errorf("error message mismatch\ngot:  %q\nwant: %q", errs, tc.wantMsg)
				}
			}
		})
	}
}
