package promote

import "strings"

// RollbackRefPrefix marks an environment whose state was set by a manual
// rollback rather than a hotfix integration branch. A rollback points an
// environment back at a prior known-good SHA and records the prior pointer in
// state.<env>.ref using this prefix, so the divergence guards treat the env as
// off-trunk (blocking forward promotion until it rejoins) without implying a
// hotfix integration branch, tags, or release drafts exist.
const RollbackRefPrefix = "rollback/"

// IsRollbackRef reports whether ref is a rollback-divergence ref (set by a
// manual rollback) rather than a hotfix integration ref. The rejoin cleanup
// uses this to skip integration-branch and hotfix-release deletion for an env
// that diverged via rollback, since no such objects were created.
func IsRollbackRef(ref string) bool {
	return strings.HasPrefix(ref, RollbackRefPrefix)
}
