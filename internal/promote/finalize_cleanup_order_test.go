package promote

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFinalizerRun_DefersLifecycleCleanup proves Run persists state without
// firing the divergence-end cleanup: the remote artifacts a rejoin removes must
// outlive the local write, because the trunk state commit that authorizes their
// removal happens afterwards. Cleanup fires only when the caller invokes it
// after the state is durably recorded.
func TestFinalizerRun_DefersLifecycleCleanup(t *testing.T) {
	configPath := divergedManifest(t)
	cleaner := &recordingCleaner{}

	fin, err := NewFinalizer(configPath, "test", WithLifecycleCleaner(cleaner))
	require.NoError(t, err)
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SourceEnv:   "dev",
			SHA:         "trunkhead",
			Version:     "v1.4.0-rc.3",
		}},
	})

	require.NoError(t, fin.Run())
	require.Empty(t, cleaner.deletedBranches,
		"Run must not delete remote artifacts before the trunk state commit")
	require.Empty(t, cleaner.cleanedReleases,
		"Run must not clean releases before the trunk state commit")

	require.NoError(t, fin.runLifecycleCleanup())
	require.Equal(t, []string{"env/test"}, cleaner.deletedBranches,
		"cleanup still fires once invoked after the state is durable")
	require.Len(t, cleaner.cleanedReleases, 1)
}

// TestPersistAndCleanup_SkipsCleanupWhenCommitFails proves the finalize command
// flow never deletes the rejoined env's remote artifacts when the trunk state
// commit fails: trunk would still record the env as diverged, pointing at an
// integration branch the cleanup would have removed.
func TestPersistAndCleanup_SkipsCleanupWhenCommitFails(t *testing.T) {
	// No GITHUB_REPOSITORY and a manifest outside any git repo: CommitAndPush
	// fails deterministically on the API path before any cleanup could run.
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "")

	configPath := divergedManifest(t)
	cleaner := &recordingCleaner{}

	fin, err := NewFinalizer(configPath, "test", WithLifecycleCleaner(cleaner))
	require.NoError(t, err)
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SourceEnv:   "dev",
			SHA:         "trunkhead",
			Version:     "v1.4.0-rc.3",
		}},
	})

	require.Error(t, persistAndCleanup(fin, true),
		"a failed trunk commit must surface as an error")
	require.Empty(t, cleaner.deletedBranches,
		"no remote artifact may be deleted when the trunk state commit failed")
	require.Empty(t, cleaner.cleanedReleases,
		"no release may be cleaned when the trunk state commit failed")
}

// TestPersistAndCleanup_NoCommit_StillCleansAfterLocalWrite covers the
// non-commit path: the local manifest write is the caller's durable record, so
// cleanup runs after it exactly as before.
func TestPersistAndCleanup_NoCommit_StillCleansAfterLocalWrite(t *testing.T) {
	configPath := divergedManifest(t)
	cleaner := &recordingCleaner{}

	fin, err := NewFinalizer(configPath, "test", WithLifecycleCleaner(cleaner))
	require.NoError(t, err)
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SourceEnv:   "dev",
			SHA:         "trunkhead",
			Version:     "v1.4.0-rc.3",
		}},
	})

	require.NoError(t, persistAndCleanup(fin, false))
	require.Equal(t, []string{"env/test"}, cleaner.deletedBranches)
	require.Len(t, cleaner.cleanedReleases, 1)
}
