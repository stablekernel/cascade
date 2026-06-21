package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasReleaseTokenApp(t *testing.T) {
	t.Run("false when unset", func(t *testing.T) {
		assert.False(t, (&TrunkConfig{}).HasReleaseTokenApp())
	})
	t.Run("true when set", func(t *testing.T) {
		cfg := &TrunkConfig{ReleaseTokenApp: &AppTokenSource{AppID: "CASCADE_APP_ID"}}
		assert.True(t, cfg.HasReleaseTokenApp())
	})
}

func TestHasStateTokenApp(t *testing.T) {
	t.Run("false when unset", func(t *testing.T) {
		assert.False(t, (&TrunkConfig{}).HasStateTokenApp())
	})
	t.Run("true when set", func(t *testing.T) {
		cfg := &TrunkConfig{StateTokenApp: &AppTokenSource{AppID: "CASCADE_APP_ID"}}
		assert.True(t, cfg.HasStateTokenApp())
	})
}

func TestReleaseTokenAppAccessors(t *testing.T) {
	t.Run("nil source returns empty", func(t *testing.T) {
		cfg := &TrunkConfig{}
		assert.Equal(t, "", cfg.GetReleaseTokenAppID())
		assert.Equal(t, "", cfg.GetReleaseTokenAppPrivateKey())
	})
	t.Run("bare names wrap as secrets", func(t *testing.T) {
		cfg := &TrunkConfig{ReleaseTokenApp: &AppTokenSource{
			AppID:      "CASCADE_APP_ID",
			PrivateKey: "CASCADE_APP_PRIVATE_KEY",
		}}
		assert.Equal(t, "${{ secrets.CASCADE_APP_ID }}", cfg.GetReleaseTokenAppID())
		assert.Equal(t, "${{ secrets.CASCADE_APP_PRIVATE_KEY }}", cfg.GetReleaseTokenAppPrivateKey())
	})
	t.Run("full expression passes through", func(t *testing.T) {
		cfg := &TrunkConfig{ReleaseTokenApp: &AppTokenSource{
			AppID:      "${{ vars.CASCADE_APP_ID }}",
			PrivateKey: "${{ secrets.CASCADE_APP_PRIVATE_KEY }}",
		}}
		assert.Equal(t, "${{ vars.CASCADE_APP_ID }}", cfg.GetReleaseTokenAppID())
		assert.Equal(t, "${{ secrets.CASCADE_APP_PRIVATE_KEY }}", cfg.GetReleaseTokenAppPrivateKey())
	})
}

func TestStateTokenAppAccessors(t *testing.T) {
	t.Run("nil source returns empty", func(t *testing.T) {
		cfg := &TrunkConfig{}
		assert.Equal(t, "", cfg.GetStateTokenAppID())
		assert.Equal(t, "", cfg.GetStateTokenAppPrivateKey())
	})
	t.Run("bare names wrap as secrets", func(t *testing.T) {
		cfg := &TrunkConfig{StateTokenApp: &AppTokenSource{
			AppID:      "CASCADE_APP_ID",
			PrivateKey: "CASCADE_APP_PRIVATE_KEY",
		}}
		assert.Equal(t, "${{ secrets.CASCADE_APP_ID }}", cfg.GetStateTokenAppID())
		assert.Equal(t, "${{ secrets.CASCADE_APP_PRIVATE_KEY }}", cfg.GetStateTokenAppPrivateKey())
	})
}

func TestValidateAppTokenSource(t *testing.T) {
	tests := []struct {
		name      string
		prefix    string
		src       *AppTokenSource
		wantErrs  int
		wantMatch string
	}{
		{
			name:     "nil is accepted",
			prefix:   "release_token_app",
			src:      nil,
			wantErrs: 0,
		},
		{
			name:     "valid bare names accepted",
			prefix:   "release_token_app",
			src:      &AppTokenSource{AppID: "CASCADE_APP_ID", PrivateKey: "CASCADE_APP_PRIVATE_KEY"},
			wantErrs: 0,
		},
		{
			name:     "valid secret expressions accepted",
			prefix:   "state_token_app",
			src:      &AppTokenSource{AppID: "${{ vars.CASCADE_APP_ID }}", PrivateKey: "${{ secrets.CASCADE_APP_PRIVATE_KEY }}"},
			wantErrs: 0,
		},
		{
			name:      "missing private_key rejected",
			prefix:    "release_token_app",
			src:       &AppTokenSource{AppID: "CASCADE_APP_ID"},
			wantErrs:  1,
			wantMatch: "release_token_app.private_key is required",
		},
		{
			name:      "missing app_id rejected",
			prefix:    "state_token_app",
			src:       &AppTokenSource{PrivateKey: "CASCADE_APP_PRIVATE_KEY"},
			wantErrs:  1,
			wantMatch: "state_token_app.app_id is required",
		},
		{
			name:      "raw key material with newline rejected",
			prefix:    "release_token_app",
			src:       &AppTokenSource{AppID: "CASCADE_APP_ID", PrivateKey: "line1\nline2"},
			wantErrs:  1,
			wantMatch: "not raw key material",
		},
		{
			name:      "PRIVATE KEY marker rejected",
			prefix:    "release_token_app",
			src:       &AppTokenSource{AppID: "CASCADE_APP_ID", PrivateKey: "-----BEGIN RSA PRIVATE KEY-----"},
			wantErrs:  1,
			wantMatch: "not raw key material",
		},
		{
			name:      "malformed bare name rejected",
			prefix:    "state_token_app",
			src:       &AppTokenSource{AppID: "not a name!", PrivateKey: "CASCADE_APP_PRIVATE_KEY"},
			wantErrs:  1,
			wantMatch: "valid GitHub Actions secret name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateAppTokenSource(tt.prefix, tt.src)
			assert.Len(t, errs, tt.wantErrs)
			if tt.wantMatch != "" {
				assert.Contains(t, joinErrs(errs), tt.wantMatch)
			}
		})
	}
}

func joinErrs(errs []string) string {
	out := ""
	for _, e := range errs {
		out += e + "\n"
	}
	return out
}
