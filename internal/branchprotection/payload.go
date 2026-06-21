// Package branchprotection emits the JSON body an operator applies to GitHub's
// branch-protection API for a cascade-managed trunk. cascade EMITS the file; the
// operator APPLIES it. cascade never calls the GitHub API.
//
// The emitted artifact is a small wrapper with two top-level keys:
//
//	{
//	  "protection":    { ... },  // the EXACT body to PUT to the branches API
//	  "operator_todo": { ... }   // companion guidance, NOT part of the PUT body
//	}
//
// An operator applies it with, for example:
//
//	cascade branch-protection | jq .protection | \
//	  gh api -X PUT repos/OWNER/REPO/branches/main/protection --input -
//
// The safety invariant is that the ".protection" object, applied verbatim, never
// creates a required status check that can never report. Only the two cascade
// controlled steps jobs (Setup and Finalize) are required, because cascade knows
// their exact check-run names and both run unconditionally on every run. The
// reusable-workflow caller jobs (validate, build, deploy) are deliberately left
// out of the required contexts: cascade knows their display-name prefix but not
// the inner job name that GitHub appends to form the real context, so requiring a
// bare prefix would block every pull request. Those prefixes are surfaced under
// operator_todo.complete_these_contexts as "<DisplayName> / <inner-job>"
// placeholders for the operator to complete once they know their reusable
// workflow's inner job names.
package branchprotection

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/generate"
)

// Default protection settings. These mirror the hotfix branch-protection
// advisory (required_pull_request_reviews.required_approving_review_count=1,
// enforce_admins=true) so cascade never emits two contradictory recommendations.
const (
	defaultRequiredApprovingReviewCount = 1
	defaultStrictStatusChecks           = true
	defaultDismissStaleReviews          = true
	defaultRequireCodeOwnerReviews      = false
	defaultEnforceAdmins                = true
	defaultRequiredLinearHistory        = true
	defaultAllowForcePushes             = false
	defaultAllowDeletions               = false
	defaultRequiredConversationResolve  = true
)

// innerJobPlaceholder is the token appended to a reusable-caller DisplayName so
// the operator can see exactly what to fill in. GitHub forms the real check-run
// context as "<DisplayName> / <inner-job-name>", and cascade does not author the
// reusable workflow, so it cannot know the inner job name.
const innerJobPlaceholder = "<inner-job>"

// Payload is the full emitted artifact. The "protection" object is the exact PUT
// body; "operator_todo" is sibling guidance that must never be sent to GitHub.
type Payload struct {
	Protection   Protection   `json:"protection"`
	OperatorTodo OperatorTodo `json:"operator_todo"`
}

// Protection is the body an operator PUTs to
// /repos/{owner}/{repo}/branches/{branch}/protection. Every field is explicit so
// the marshaled output is byte-stable for a given manifest.
type Protection struct {
	RequiredStatusChecks           RequiredStatusChecks       `json:"required_status_checks"`
	EnforceAdmins                  bool                       `json:"enforce_admins"`
	RequiredPullRequestReviews     RequiredPullRequestReviews `json:"required_pull_request_reviews"`
	Restrictions                   *Restrictions              `json:"restrictions"`
	RequiredLinearHistory          bool                       `json:"required_linear_history"`
	AllowForcePushes               bool                       `json:"allow_force_pushes"`
	AllowDeletions                 bool                       `json:"allow_deletions"`
	RequiredConversationResolution bool                       `json:"required_conversation_resolution"`
}

// RequiredStatusChecks lists the checks GitHub requires before merge. contexts
// holds ONLY cascade-controlled steps-job names whose check-run context cascade
// knows exactly, kept sorted for deterministic output.
type RequiredStatusChecks struct {
	Strict   bool     `json:"strict"`
	Contexts []string `json:"contexts"`
}

// RequiredPullRequestReviews mirrors the GitHub API review-protection block.
type RequiredPullRequestReviews struct {
	RequiredApprovingReviewCount int  `json:"required_approving_review_count"`
	DismissStaleReviews          bool `json:"dismiss_stale_reviews"`
	RequireCodeOwnerReviews      bool `json:"require_code_owner_reviews"`
}

// Restrictions models the push-restriction block. GitHub requires the key to be
// present; a nil *Restrictions marshals to null, meaning no push restriction.
type Restrictions struct {
	Users []string `json:"users"`
	Teams []string `json:"teams"`
	Apps  []string `json:"apps"`
}

// OperatorTodo is companion guidance emitted alongside the PUT body. It is NOT
// part of the body GitHub accepts, so it is carried as a sibling key the operator
// strips off (jq .protection) before applying.
type OperatorTodo struct {
	Note                  string   `json:"note"`
	CompleteTheseContexts []string `json:"complete_these_contexts"`
}

// Build assembles the Payload from a resolved manifest. The required contexts are
// exactly the cascade-controlled steps jobs (Setup and Finalize), referenced from
// generate.SetupJobName and generate.FinalizeJobName so a rename in the generator
// updates both the workflow and these contexts together. Every reusable-workflow
// caller job (validate, build, deploy) is surfaced under operator_todo as a
// "<DisplayName> / <inner-job>" placeholder rather than required directly, because
// requiring a context cascade cannot fully name would block every pull request.
func Build(cfg *config.TrunkConfig, branch string) Payload {
	contexts := []string{generate.SetupJobName, generate.FinalizeJobName}
	sort.Strings(contexts)

	graph := generate.BuildDependencyGraph(cfg)
	todo := make([]string, 0, len(graph.Order))
	for _, jobID := range graph.Order {
		node := graph.Nodes[jobID]
		todo = append(todo, fmt.Sprintf("%s / %s", node.DisplayName, innerJobPlaceholder))
	}
	sort.Strings(todo)

	return Payload{
		Protection: Protection{
			RequiredStatusChecks: RequiredStatusChecks{
				Strict:   defaultStrictStatusChecks,
				Contexts: contexts,
			},
			EnforceAdmins: defaultEnforceAdmins,
			RequiredPullRequestReviews: RequiredPullRequestReviews{
				RequiredApprovingReviewCount: defaultRequiredApprovingReviewCount,
				DismissStaleReviews:          defaultDismissStaleReviews,
				RequireCodeOwnerReviews:      defaultRequireCodeOwnerReviews,
			},
			Restrictions:                   nil,
			RequiredLinearHistory:          defaultRequiredLinearHistory,
			AllowForcePushes:               defaultAllowForcePushes,
			AllowDeletions:                 defaultAllowDeletions,
			RequiredConversationResolution: defaultRequiredConversationResolve,
		},
		OperatorTodo: OperatorTodo{
			Note:                  operatorNote(branch),
			CompleteTheseContexts: todo,
		},
	}
}

// operatorNote returns the human guidance string for the given branch. It states
// the safe-by-construction contract and tells the operator how to apply the body.
func operatorNote(branch string) string {
	return fmt.Sprintf(
		"PUT only the .protection object to "+
			"repos/{owner}/{repo}/branches/%s/protection (for example: "+
			"jq .protection | gh api -X PUT ... --input -). The required contexts "+
			"contain only the cascade-controlled Setup and Finalize jobs, which run "+
			"on every pipeline run, so .protection applied as-is never blocks a pull "+
			"request. complete_these_contexts lists the reusable-workflow caller jobs "+
			"as \"<DisplayName> / <inner-job>\" placeholders. Replace <inner-job> with "+
			"the job name inside each reusable workflow and add the completed context "+
			"strings to required_status_checks.contexts when you want them required.",
		branch,
	)
}

// Marshal renders the payload as indented JSON with exactly one trailing newline.
// The same manifest always produces byte-identical output.
func Marshal(p Payload) ([]byte, error) {
	out, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling branch-protection payload: %w", err)
	}
	out = append(out, '\n')
	return out, nil
}
