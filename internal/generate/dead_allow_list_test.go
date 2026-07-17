package generate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAssertNoDeadAllowList exercises the guard directly. The cases below are
// unreachable through the CLI today, because every emission site that renders a
// block-style allow-list is length-guarded, so a config-driven test could not
// reach them. The guard's whole purpose is to hold when a future emission site
// is not guarded, which makes the function itself the unit under test.
func TestAssertNoDeadAllowList(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		wantErr string
	}{
		{
			name:    "flow-style empty branches",
			out:     "on:\n  push:\n    branches: []\n",
			wantErr: "branches",
		},
		{
			name:    "block-style empty branches",
			out:     "on:\n  push:\n    branches:\n  workflow_dispatch:\n",
			wantErr: "branches",
		},
		{
			// Both keys are block style and the first one has items. A scan that
			// seeks only the first "branches:" line match lands on the populated
			// one, judges it live, and never examines the dead one below it.
			name: "empty branches after a populated one",
			out: "on:\n  push:\n    branches:\n      - main\n" +
				"  pull_request:\n    branches:\n",
			wantErr: "branches",
		},
		{
			name:    "block-style empty paths",
			out:     "on:\n  push:\n    branches: [main]\n    paths:\n",
			wantErr: "paths",
		},
		{
			name:    "block-style empty tags",
			out:     "on:\n  push:\n    tags:\n",
			wantErr: "tags",
		},
		{
			name:    "block-style empty paths-ignore",
			out:     "on:\n  push:\n    branches: [main]\n    paths-ignore:\n",
			wantErr: "paths-ignore",
		},
		{
			name: "populated block-style branches is fine",
			out:  "on:\n  push:\n    branches:\n      - main\n      - release/*\n",
		},
		{
			name: "populated block-style paths is fine",
			out:  "on:\n  push:\n    branches: [main]\n    paths:\n      - src/**\n",
		},
		{
			name: "populated flow-style branches is fine",
			out:  "on:\n  push:\n    branches: [main]\n  workflow_dispatch:\n",
		},
		{
			name: "no push trigger at all is fine",
			out:  "on:\n  workflow_dispatch:\n    inputs:\n      dry_run:\n        type: boolean\n",
		},
		{
			// A mapping key that merely ends in a guarded name must not trip the
			// scan, and neither may a value that happens to mention one.
			name: "unrelated keys and values are fine",
			out:  "jobs:\n  x:\n    steps:\n      - run: echo \"branches:\"\n        name: list-branches\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := assertNoDeadAllowList(tt.out)
			if tt.wantErr == "" {
				assert.NoError(t, err, "must not flag a workflow that runs")
				return
			}
			require.Error(t, err, "an empty allow-list must be rejected")
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
