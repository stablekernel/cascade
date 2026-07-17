package harness

import (
	"fmt"
	"sync"
)

// ExecutionContext tracks state across multi-step scenario execution
type ExecutionContext struct {
	mu sync.RWMutex
	// commitSteps counts the `action: commit` steps a scenario has run, and is
	// the sole source of the commitN alias numbers. It is deliberately not
	// len(commitSeq): the harness also records aliases for commits nobody wrote
	// as a step, such as a merged hotfix's env branch tip, and those must not
	// shift the numbering of the commits a scenario author did write.
	commitSteps    int
	commits        map[string]string                          // commit reference -> SHA
	commitSeq      []string                                   // ordered commit SHAs
	commitMessages map[string]string                          // SHA -> commit message
	state          map[string]*EnvState                       // env -> state
	tags           map[string]bool                            // tag -> exists
	releases       map[string]*ReleaseInfo                    // tag -> release info
	externalState  map[string]map[string]*ExternalDeployState // env -> deploy -> state
}

// EnvState tracks environment state
type EnvState struct {
	SHA     string
	Version string
	Deploys map[string]*DeployState
	// Ref is the integration branch the environment tracks instead of trunk.
	Ref string
	// BaseSHA is the trunk anchor the integration branch diverged from.
	BaseSHA string
	// Patches lists the patch commit SHAs applied on top of BaseSHA.
	Patches []string
	// PreviousVersion is the version held before the latest divergence update.
	// Harness-side only; not read back from the product manifest.
	PreviousVersion string
	// PreviousVersions lists the versions recorded in the environment's
	// deploy-history ring (state.<env>.previous, newest first), synced from the
	// manifest. It lets a scenario assert a later promotion carried the recorded
	// ring forward instead of rebuilding the env leaf from empty.
	PreviousVersions []string
}

// DeployState tracks per-deploy state
type DeployState struct {
	SHA string
}

// ReleaseInfo tracks release state
type ReleaseInfo struct {
	Tag        string
	SHA        string
	Prerelease bool
	Draft      bool
	Latest     bool
}

// ExternalDeployState tracks state of an external deploy from a satellite repo
type ExternalDeployState struct {
	SHA     string
	Version string
	Repo    string
}

// NewExecutionContext creates a new execution context
func NewExecutionContext() *ExecutionContext {
	return &ExecutionContext{
		commits:        make(map[string]string),
		commitMessages: make(map[string]string),
		state:          make(map[string]*EnvState),
		tags:           make(map[string]bool),
		releases:       make(map[string]*ReleaseInfo),
		externalState:  make(map[string]map[string]*ExternalDeployState),
	}
}

// RecordCommit records a commit SHA with a reference name
func (c *ExecutionContext) RecordCommit(ref, sha string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commits[ref] = sha
	c.commitSeq = append(c.commitSeq, sha)
}

// NextCommitRef reserves and returns the alias for the next `action: commit`
// step: commit1, commit2, and so on, 1-indexed over commit steps alone. This is
// the numbering a scenario author counts by eye when they write commit3, so
// nothing but a commit step may advance it.
func (c *ExecutionContext) NextCommitRef() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commitSteps++
	return fmt.Sprintf("commit%d", c.commitSteps)
}

// ResolveSHA resolves a commit reference to its SHA
func (c *ExecutionContext) ResolveSHA(ref string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.commits[ref]
}

// GetCommitCount returns the number of commits tracked, counting every SHA
// recorded, including ones no `action: commit` step created.
//
// It is not the commitN alias number: deriving that from this count is what
// made a merged hotfix's env branch tip shift every later alias by one. Use
// NextCommitRef for the alias.
func (c *ExecutionContext) GetCommitCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.commitSeq)
}

// RecordCommitMessage records a commit message for breaking change detection
func (c *ExecutionContext) RecordCommitMessage(sha, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commitMessages[sha] = message
}

// HasBreakingChange checks if any commit since the given SHA has a breaking change
// indicated by ! in the commit type (e.g., "feat!:", "fix!:")
func (c *ExecutionContext) HasBreakingChange(sinceSHA string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	startIdx := c.getStartIndex(sinceSHA)

	// Check commits after sinceSHA for breaking changes
	for i := startIdx; i < len(c.commitSeq); i++ {
		sha := c.commitSeq[i]
		if msg, ok := c.commitMessages[sha]; ok {
			// Check for breaking change indicator (! before :)
			for j := 0; j < len(msg); j++ {
				if msg[j] == ':' {
					break
				}
				if msg[j] == '!' {
					return true
				}
			}
		}
	}
	return false
}

// HasFeatureCommit checks if any commit since the given SHA is a feature commit (feat:)
func (c *ExecutionContext) HasFeatureCommit(sinceSHA string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	startIdx := c.getStartIndex(sinceSHA)

	// Check commits after sinceSHA for feature commits
	for i := startIdx; i < len(c.commitSeq); i++ {
		sha := c.commitSeq[i]
		if msg, ok := c.commitMessages[sha]; ok {
			if len(msg) >= 5 && (msg[:5] == "feat:" || msg[:5] == "feat!") {
				return true
			}
			if len(msg) >= 4 && msg[:4] == "feat" {
				// Check for feat( or feat!
				for j := 4; j < len(msg); j++ {
					if msg[j] == ':' || msg[j] == '!' || msg[j] == '(' {
						return true
					}
					if msg[j] != ' ' {
						break
					}
				}
			}
		}
	}
	return false
}

// getStartIndex finds the index to start checking from in the commit sequence
func (c *ExecutionContext) getStartIndex(sinceSHA string) int {
	if sinceSHA == "" {
		return 0
	}
	for i, sha := range c.commitSeq {
		if sha == sinceSHA {
			return i + 1
		}
	}
	return 0
}

// RecordState records environment state
func (c *ExecutionContext) RecordState(env, sha, version string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state[env] == nil {
		c.state[env] = &EnvState{Deploys: make(map[string]*DeployState)}
	}
	c.state[env].SHA = sha
	c.state[env].Version = version
}

// RecordStateDivergence records the integration-branch divergence fields for an
// environment (ref, base SHA, patch list, and pre-divergence version) without
// disturbing the recorded SHA/Version. Used by manifest sync and setup staging
// so divergence assertions can observe a hotfixed environment.
func (c *ExecutionContext) RecordStateDivergence(env, ref, baseSHA string, patches []string, previousVersion string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state[env] == nil {
		c.state[env] = &EnvState{Deploys: make(map[string]*DeployState)}
	}
	c.state[env].Ref = ref
	c.state[env].BaseSHA = baseSHA
	c.state[env].Patches = append([]string(nil), patches...)
	c.state[env].PreviousVersion = previousVersion
}

// RecordStatePreviousVersions records the versions in an environment's
// deploy-history ring (state.<env>.previous, newest first) without disturbing
// the recorded SHA/Version. Used by manifest sync so previous_contains
// assertions can observe that a promotion carried the recorded ring forward.
func (c *ExecutionContext) RecordStatePreviousVersions(env string, versions []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state[env] == nil {
		c.state[env] = &EnvState{Deploys: make(map[string]*DeployState)}
	}
	c.state[env].PreviousVersions = append([]string(nil), versions...)
}

// ClearState removes all env state. Used by sync routines that need to
// rebuild ctx from an authoritative source (the manifest), so deletions in
// that source (e.g. finalize wiping state[prerelease] on publish) are
// reflected rather than leaving stale ctx entries.
func (c *ExecutionContext) ClearState() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = make(map[string]*EnvState)
}

// GetState returns the current state for an environment
func (c *ExecutionContext) GetState(env string) *EnvState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if s := c.state[env]; s != nil {
		return s
	}
	return &EnvState{}
}

// HasState reports whether the context has a recorded state entry for env.
// GetState cannot answer this: it returns a zero EnvState for an unknown env so
// callers that legitimately probe for absence stay simple, which also means a
// typo'd env name reads back as an empty state instead of an error. Assertions
// use HasState to tell "the env holds nothing" apart from "the env does not
// exist".
func (c *ExecutionContext) HasState(env string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state[env] != nil
}

// RecordDeployState records per-deploy state
func (c *ExecutionContext) RecordDeployState(env, deploy, sha string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state[env] == nil {
		c.state[env] = &EnvState{Deploys: make(map[string]*DeployState)}
	}
	if c.state[env].Deploys == nil {
		c.state[env].Deploys = make(map[string]*DeployState)
	}
	c.state[env].Deploys[deploy] = &DeployState{SHA: sha}
}

// RecordTag records a tag existence
func (c *ExecutionContext) RecordTag(tag string, exists bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tags[tag] = exists
}

// HasTag checks if a tag exists
func (c *ExecutionContext) HasTag(tag string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tags[tag]
}

// ClearTags removes all recorded tags (used before re-syncing from source)
func (c *ExecutionContext) ClearTags() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tags = make(map[string]bool)
}

// RecordRelease records release info
func (c *ExecutionContext) RecordRelease(info *ReleaseInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releases[info.Tag] = info
}

// GetRelease returns release info for a tag
func (c *ExecutionContext) GetRelease(tag string) *ReleaseInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.releases[tag]
}

// WipeState removes state for an environment
func (c *ExecutionContext) WipeState(env string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.state, env)
}

// Clone creates a snapshot of current state for comparison
func (c *ExecutionContext) Clone() *ExecutionContext {
	c.mu.RLock()
	defer c.mu.RUnlock()

	clone := NewExecutionContext()
	for k, v := range c.commits {
		clone.commits[k] = v
	}
	clone.commitSteps = c.commitSteps
	clone.commitSeq = append([]string{}, c.commitSeq...)
	for k, v := range c.state {
		clone.state[k] = &EnvState{
			SHA:              v.SHA,
			Version:          v.Version,
			Deploys:          make(map[string]*DeployState),
			Ref:              v.Ref,
			BaseSHA:          v.BaseSHA,
			Patches:          append([]string(nil), v.Patches...),
			PreviousVersion:  v.PreviousVersion,
			PreviousVersions: append([]string(nil), v.PreviousVersions...),
		}
		for dk, dv := range v.Deploys {
			clone.state[k].Deploys[dk] = &DeployState{SHA: dv.SHA}
		}
	}
	for k, v := range c.tags {
		clone.tags[k] = v
	}
	for k, v := range c.releases {
		clone.releases[k] = &ReleaseInfo{
			Tag:        v.Tag,
			SHA:        v.SHA,
			Prerelease: v.Prerelease,
			Draft:      v.Draft,
			Latest:     v.Latest,
		}
	}
	for env, deploys := range c.externalState {
		clone.externalState[env] = make(map[string]*ExternalDeployState)
		for name, state := range deploys {
			clone.externalState[env][name] = &ExternalDeployState{
				SHA:     state.SHA,
				Version: state.Version,
				Repo:    state.Repo,
			}
		}
	}
	return clone
}

// RecordExternalDeployState records external deploy state from a satellite repo
func (c *ExecutionContext) RecordExternalDeployState(env, deploy, sha, version string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.externalState[env] == nil {
		c.externalState[env] = make(map[string]*ExternalDeployState)
	}
	c.externalState[env][deploy] = &ExternalDeployState{
		SHA:     sha,
		Version: version,
	}
}

// GetExternalDeployState returns external deploy state
func (c *ExecutionContext) GetExternalDeployState(env, deploy string) *ExternalDeployState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.externalState[env] == nil {
		return nil
	}
	return c.externalState[env][deploy]
}

// GetAllExternalState returns all external state for an environment
func (c *ExecutionContext) GetAllExternalState(env string) map[string]*ExternalDeployState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.externalState[env] == nil {
		return nil
	}
	// Return a copy to avoid concurrent access issues
	result := make(map[string]*ExternalDeployState)
	for k, v := range c.externalState[env] {
		result[k] = &ExternalDeployState{
			SHA:     v.SHA,
			Version: v.Version,
			Repo:    v.Repo,
		}
	}
	return result
}
