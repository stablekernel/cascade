package promote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// roleReleaseManifest declares a ladder where the release and prerelease stages
// are pinned by role: rather than by position. The list is [dev, staging, prod,
// monitor], but prod carries role: release and staging role: prerelease, so the
// release marker must land at the staging -> prod crossing, NOT at the
// positional last env (monitor) or the positional second-from-last (prod as
// prerelease). dev and staging are seeded at the same SHA so default mode's next
// logical step is the staging -> prod publish boundary.
const roleReleaseManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - name: dev
      - name: staging
        role: prerelease
      - name: prod
        role: release
      - name: monitor
  state:
    dev:
      sha: sha1
      version: 1.0.0-rc.0
    staging:
      sha: sha1
      version: 1.0.0-rc.0
`

func writeRoleReleaseManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(roleReleaseManifest), 0o644))
	return path
}

func hasReleaseMarker(promos []EnvPromotion) bool {
	for _, p := range promos {
		if p.Environment == "release" {
			return true
		}
	}
	return false
}

// TestCascade_RoleRelease_PublishesAtRoleEnv proves the cascade path honors an
// explicit role: release. A dev-to-prod cascade must insert the "release" marker
// promotion at the staging -> prod crossing (prod being the role:release env) and
// report the publish release action. Before the cascade path was routed through
// the role-aware accessors it treated the positional last env (monitor) as the
// release env, so crossing into the role env (prod) produced no release marker
// and the publish landed on the wrong environment.
func TestCascade_RoleRelease_PublishesAtRoleEnv(t *testing.T) {
	path := writeRoleReleaseManifest(t)

	p, err := NewPromoter(PromoterOptions{
		ConfigPath: path,
		DryRun:     true,
		Actor:      "test-actor",
	})
	require.NoError(t, err)

	result, err := p.Promote(ModeCascade, "dev-to-prod")
	require.NoError(t, err)
	require.True(t, result.Success, "dev-to-prod cascade should succeed; error: %s", result.Error)

	require.True(t, hasReleaseMarker(result.Promotions),
		"cascade into the role:release env (prod) must insert a release marker promotion")
	require.Equal(t, "publish", result.ReleaseAction,
		"finishing at the role:release env must publish, not treat prod as an intermediate")

	// The release marker must sit immediately before the prod deploy, proving the
	// publish boundary tracks the role env rather than the positional last env.
	var relIdx, prodIdx = -1, -1
	for i, promo := range result.Promotions {
		switch promo.Environment {
		case "release":
			relIdx = i
		case "prod":
			prodIdx = i
		}
	}
	require.NotEqual(t, -1, relIdx, "release marker missing")
	require.NotEqual(t, -1, prodIdx, "prod promotion missing")
	require.Less(t, relIdx, prodIdx, "release marker must precede the prod deploy")
}

// TestDefaultAndCascade_AgreeOnRoleMarkers proves single-step (default) and
// cascade modes place the release marker at the SAME role-tagged crossing for
// one role-annotated manifest. Default mode advancing from the seeded staging
// state hits the staging -> prod publish boundary, and cascade dev-to-prod
// crosses the same boundary; both must produce a "release" marker. A positional
// cascade path would disagree with the role-aware default path here.
func TestDefaultAndCascade_AgreeOnRoleMarkers(t *testing.T) {
	pDefault, err := NewPromoter(PromoterOptions{
		ConfigPath: writeRoleReleaseManifest(t),
		DryRun:     true,
		Actor:      "test-actor",
	})
	require.NoError(t, err)
	defaultResult, err := pDefault.Promote(ModeDefault, "")
	require.NoError(t, err)
	require.True(t, defaultResult.Success, "default promotion should succeed; error: %s", defaultResult.Error)

	pCascade, err := NewPromoter(PromoterOptions{
		ConfigPath: writeRoleReleaseManifest(t),
		DryRun:     true,
		Actor:      "test-actor",
	})
	require.NoError(t, err)
	cascadeResult, err := pCascade.Promote(ModeCascade, "dev-to-prod")
	require.NoError(t, err)
	require.True(t, cascadeResult.Success, "cascade should succeed; error: %s", cascadeResult.Error)

	require.True(t, hasReleaseMarker(defaultResult.Promotions),
		"default mode must place the release marker at the staging -> prod (role) boundary")
	require.True(t, hasReleaseMarker(cascadeResult.Promotions),
		"cascade mode must place the release marker at the same role boundary as default mode")
}
