// Package environments emits a per-environment configuration file an operator
// applies to GitHub's Environments REST API. cascade EMITS the file; the
// operator APPLIES it (gh api / Terraform). cascade never calls the GitHub API.
//
// The emitted artifact is a small wrapper:
//
//	{
//	  "environments": [ { ... }, { ... } ]  // one entry per manifest environment,
//	                                         // in the manifest's environments order
//	}
//
// Each entry pairs the directly-appliable Environments REST body with companion
// operator guidance:
//
//	{
//	  "name": "prod",                 // the cascade environment name
//	  "gha_environment": "production",// the GitHub Environment to configure
//	  "environment": { ... },         // the body to PUT to the environments API
//	  "operator_todo": { ... }        // guidance that is NOT part of the PUT body
//	}
//
// The split mirrors the branch-protection command. The "environment" object is
// the exact body for
//
//	PUT /repos/{owner}/{repo}/environments/{gha_environment}
//
// for the fields cascade can fully form from the manifest (wait_timer and
// deployment_branch_policy). Required reviewers are surfaced under
// operator_todo, not inside "environment": the REST API requires a numeric
// reviewer id, and the manifest carries only slugs, so the operator resolves
// each slug to an id before adding it. Expected env-scoped secret and variable
// NAMES are also operator guidance: they are created through the separate
// environment-secrets and environment-variables APIs, and cascade emits names
// only, never values.
package environments

import (
	"encoding/json"
	"fmt"

	"github.com/stablekernel/cascade/internal/config"
)

// Payload is the full emitted artifact: one entry per manifest environment, in
// the manifest's environments order.
type Payload struct {
	Environments []Environment `json:"environments"`
}

// Environment is the per-environment config block. "environment" is the exact
// PUT body for the fields cascade can form from the manifest; "operator_todo"
// is sibling guidance that must never be sent to GitHub verbatim.
type Environment struct {
	// Name is the cascade environment name (from the manifest environments list).
	Name string `json:"name"`
	// GHAEnvironment is the GitHub Environment to configure. It defaults to Name
	// when the manifest does not set environment_config.<name>.gha_environment.
	GHAEnvironment string `json:"gha_environment"`
	// Environment is the body to PUT to the environments REST API.
	Environment EnvironmentBody `json:"environment"`
	// OperatorTodo is companion guidance, NOT part of the PUT body.
	OperatorTodo OperatorTodo `json:"operator_todo"`
}

// EnvironmentBody is the body an operator PUTs to
// /repos/{owner}/{repo}/environments/{environment_name}. Only the fields
// cascade can fully form from the manifest appear here. Reviewers are omitted on
// purpose (the API needs numeric ids; see OperatorTodo.RequiredReviewers).
type EnvironmentBody struct {
	// WaitTimer is the deploy delay in minutes (0..43200). It is always emitted
	// so the body is byte-stable; 0 means no wait.
	WaitTimer int `json:"wait_timer"`
	// DeploymentBranchPolicy selects which branches may deploy. A nil pointer
	// marshals to null, GitHub's "all branches" value.
	DeploymentBranchPolicy *DeploymentBranchPolicy `json:"deployment_branch_policy"`
}

// DeploymentBranchPolicy mirrors GitHub's deployment_branch_policy object.
// Exactly one of ProtectedBranches and CustomBranchPolicies is true; GitHub
// rejects a body where both are equal.
type DeploymentBranchPolicy struct {
	ProtectedBranches    bool `json:"protected_branches"`
	CustomBranchPolicies bool `json:"custom_branch_policies"`
}

// OperatorTodo is companion guidance emitted alongside the PUT body. It is NOT
// accepted by the environments PUT endpoint and must be applied through the
// steps it describes rather than sent to GitHub verbatim.
type OperatorTodo struct {
	// Note is human guidance describing how to apply this entry.
	Note string `json:"note"`
	// RequiredReviewers lists the user or team slugs to add as reviewers. The
	// REST API needs each reviewer's numeric id, which the manifest does not
	// carry, so the operator resolves these slugs to ids before applying.
	RequiredReviewers []string `json:"required_reviewers,omitempty"`
	// BranchPatterns lists branch name patterns to create as
	// deployment-branch-policies (custom policy only).
	BranchPatterns []string `json:"branch_patterns,omitempty"`
	// TagPatterns lists tag name patterns to create as deployment-branch-policies
	// of type tag (custom policy only).
	TagPatterns []string `json:"tag_patterns,omitempty"`
	// Secrets lists the EXPECTED env-scoped secret NAMES to create through the
	// environment-secrets API. Names only, never values.
	Secrets []string `json:"secrets,omitempty"`
	// Variables lists the EXPECTED env-scoped variable NAMES to create through
	// the environment-variables API. Names only, never values.
	Variables []string `json:"variables,omitempty"`
}

// Build assembles the Payload from a resolved manifest. It walks
// cfg.Environments in order so the output is stable and matches the manifest's
// declared environment order. For each environment it reads the optional
// environment_config.<name> block; an absent block yields an entry whose
// gha_environment defaults to the environment name, an empty wait timer, an
// "all branches" policy, and no reviewers, secrets, or variables.
func Build(cfg *config.TrunkConfig) Payload {
	out := Payload{Environments: make([]Environment, 0, len(cfg.Environments))}
	for _, name := range cfg.Environments {
		ec := cfg.EnvironmentConfig[name]

		ghaEnv := ec.GHAEnvironment
		if ghaEnv == "" {
			ghaEnv = name
		}

		out.Environments = append(out.Environments, Environment{
			Name:           name,
			GHAEnvironment: ghaEnv,
			Environment: EnvironmentBody{
				WaitTimer:              ec.WaitTimerMinutes(),
				DeploymentBranchPolicy: branchPolicy(ec.BranchPolicy),
			},
			OperatorTodo: OperatorTodo{
				Note:              operatorNote(ghaEnv),
				RequiredReviewers: nonEmpty(ec.RequiredReviewers),
				BranchPatterns:    nonEmpty(ec.BranchPatterns),
				TagPatterns:       nonEmpty(ec.TagPatterns),
				Secrets:           nonEmpty(ec.Secrets),
				Variables:         nonEmpty(ec.Variables),
			},
		})
	}
	return out
}

// branchPolicy maps a manifest branch_policy string onto GitHub's
// deployment_branch_policy object. An empty policy or the "all" policy returns
// nil, which marshals to null (all branches may deploy).
func branchPolicy(policy string) *DeploymentBranchPolicy {
	switch policy {
	case config.EnvBranchPolicyProtected:
		return &DeploymentBranchPolicy{ProtectedBranches: true, CustomBranchPolicies: false}
	case config.EnvBranchPolicyCustom:
		return &DeploymentBranchPolicy{ProtectedBranches: false, CustomBranchPolicies: true}
	default: // "" or EnvBranchPolicyAll
		return nil
	}
}

// nonEmpty returns s unchanged when it has elements, or nil when it is empty, so
// an empty slice marshals away (omitempty) instead of as "[]".
func nonEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

// operatorNote returns the human guidance string for an environment. It states
// the emit-not-apply contract and how to apply the PUT body.
func operatorNote(ghaEnv string) string {
	return fmt.Sprintf(
		"PUT the .environment object to "+
			"repos/{owner}/{repo}/environments/%s (for example: "+
			"jq .environment | gh api -X PUT ... --input -). cascade emits this "+
			"config; you apply it. Resolve each operator_todo.required_reviewers "+
			"slug to its numeric id and add it to the body's reviewers array, then "+
			"create the operator_todo.secrets and operator_todo.variables names "+
			"through the environment-secrets and environment-variables APIs. Those "+
			"are expected NAMES only; set their values yourself. branch_patterns and "+
			"tag_patterns are created through the deployment-branch-policies API when "+
			"branch_policy is custom.",
		ghaEnv,
	)
}

// Marshal renders the payload as indented JSON with exactly one trailing
// newline. The same manifest always produces byte-identical output: the
// environment order follows the manifest, and every emitted struct field has a
// stable JSON key order.
func Marshal(p Payload) ([]byte, error) {
	out, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling environments payload: %w", err)
	}
	out = append(out, '\n')
	return out, nil
}
