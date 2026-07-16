package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emittedShapesBase is a minimal valid manifest used to isolate the emitted-value
// shape rules: every test mutates exactly one field on a copy of this base.
func emittedShapesBase() *TrunkConfig {
	return &TrunkConfig{
		TrunkBranch:  "main",
		Environments: EnvNames("dev", "prod"),
	}
}

// componentShapesBase wraps a single valid component so per-component scope
// rules can be exercised with one mutation.
func componentShapesBase(mutate func(c *ComponentConfig)) *TrunkConfig {
	cfg := emittedShapesBase()
	comp := ComponentConfig{
		Path:       "services/api",
		TagGrammar: &TagGrammarConfig{Prefix: strptr("api-v")},
	}
	if mutate != nil {
		mutate(&comp)
	}
	cfg.Components = map[string]ComponentConfig{"api": comp}
	return cfg
}

func TestValidate_ExtraTriggersCron_RejectsMalformedExpressions(t *testing.T) {
	hostile := []string{
		"0 2 * *",           // four fields
		"0 2 * * * *",       // six fields
		"0 2 * * $(id)",     // shell metacharacters
		"0 2 * * *' ",       // apostrophe would close the emitted single quote
		"0 2 * * *\n- cron: '* * * * *'", // newline injects a sibling entry
		"",                  // empty
		"; echo pwned",      // injection payload
	}
	for _, cron := range hostile {
		t.Run(fmt.Sprintf("top-level %q", cron), func(t *testing.T) {
			cfg := emittedShapesBase()
			cfg.ExtraTriggers = &ExtraTriggers{Schedule: []ScheduleEntry{{Cron: cron}}}
			errs := Validate(cfg)
			assert.NotEmpty(t, errs, "cron %q must be rejected", cron)
			assert.Contains(t, strings.Join(errs, "\n"), "extra_triggers.schedule[0].cron")
		})
		t.Run(fmt.Sprintf("component %q", cron), func(t *testing.T) {
			cfg := componentShapesBase(func(c *ComponentConfig) {
				c.ExtraTriggers = &ExtraTriggers{Schedule: []ScheduleEntry{{Cron: cron}}}
			})
			errs := Validate(cfg)
			assert.NotEmpty(t, errs, "component cron %q must be rejected", cron)
		})
	}
}

func TestValidate_ExtraTriggersCron_AcceptsGHACronShapes(t *testing.T) {
	good := []string{
		"0 2 * * *",
		"*/15 * * * *",
		"30 5,17 * * 1-5",
		"0 7 * * MON,WED",
		"0 0 1 JAN-JUN *",
	}
	for _, cron := range good {
		cfg := emittedShapesBase()
		cfg.ExtraTriggers = &ExtraTriggers{Schedule: []ScheduleEntry{{Cron: cron}}}
		assert.Empty(t, Validate(cfg), "cron %q is a legitimate GHA cron and must pass", cron)
	}
}

func TestValidate_ExtraTriggersRepositoryDispatchTypes_RejectsUnsafeNames(t *testing.T) {
	hostile := []string{"deploy: now", "x\ny", "#x", "a b", "  "}
	for _, typ := range hostile {
		t.Run(fmt.Sprintf("top-level %q", typ), func(t *testing.T) {
			cfg := emittedShapesBase()
			cfg.ExtraTriggers = &ExtraTriggers{
				RepositoryDispatch: &RepositoryDispatchTrigger{Types: []string{typ}},
			}
			errs := Validate(cfg)
			assert.NotEmpty(t, errs, "repository_dispatch type %q must be rejected", typ)
			assert.Contains(t, strings.Join(errs, "\n"), "extra_triggers.repository_dispatch.types")
		})
		t.Run(fmt.Sprintf("component %q", typ), func(t *testing.T) {
			cfg := componentShapesBase(func(c *ComponentConfig) {
				c.ExtraTriggers = &ExtraTriggers{
					RepositoryDispatch: &RepositoryDispatchTrigger{Types: []string{typ}},
				}
			})
			assert.NotEmpty(t, Validate(cfg), "component repository_dispatch type %q must be rejected", typ)
		})
	}
}

func TestValidate_ExtraTriggersWorkflowRunTypes_RejectsUnsafeNames(t *testing.T) {
	for _, typ := range []string{"completed: yes", "x\ny", "#x"} {
		cfg := emittedShapesBase()
		cfg.ExtraTriggers = &ExtraTriggers{
			WorkflowRun: &WorkflowRunTrigger{Workflows: []string{"Upstream CI"}, Types: []string{typ}},
		}
		errs := Validate(cfg)
		assert.NotEmpty(t, errs, "workflow_run type %q must be rejected", typ)
		assert.Contains(t, strings.Join(errs, "\n"), "extra_triggers.workflow_run.types")
	}
}

func TestValidate_ExtraTriggersWorkflowRunWorkflows_RejectsNewlineAndBlank(t *testing.T) {
	for _, name := range []string{"CI\n- pwned", "a\rb", "", "   "} {
		cfg := emittedShapesBase()
		cfg.ExtraTriggers = &ExtraTriggers{
			WorkflowRun: &WorkflowRunTrigger{Workflows: []string{name}, Types: []string{"completed"}},
		}
		errs := Validate(cfg)
		assert.NotEmpty(t, errs, "workflow_run workflow name %q must be rejected", name)
		assert.Contains(t, strings.Join(errs, "\n"), "extra_triggers.workflow_run.workflows")
	}
}

func TestValidate_ExtraTriggersWorkflowRunWorkflows_ApostropheNameAccepted(t *testing.T) {
	// Workflow display names are arbitrary prose; an apostrophe is legitimate
	// and must survive (the generator single-quote-escapes it at emit time).
	cfg := emittedShapesBase()
	cfg.ExtraTriggers = &ExtraTriggers{
		WorkflowRun: &WorkflowRunTrigger{Workflows: []string{"Bob's CI"}, Types: []string{"completed"}},
	}
	assert.Empty(t, Validate(cfg))
}

func TestValidate_TagGrammarPrefix_RejectsLeadingHyphen(t *testing.T) {
	// The prefix reaches `git tag -l <prefix>*` argv, where a leading hyphen
	// parses as a flag (exit 129).
	cfg := emittedShapesBase()
	cfg.TagGrammar = &TagGrammarConfig{Prefix: strptr("-rc")}
	errs := Validate(cfg)
	assert.NotEmpty(t, errs, "a leading-hyphen top-level tag prefix must be rejected")
	assert.Contains(t, strings.Join(errs, "\n"), "tag_grammar.prefix")
}

func TestValidate_ComponentTagGrammarPrefix_ShapeEnforced(t *testing.T) {
	cases := []string{"-rc", "a'b", "a b", "a$b"}
	for _, prefix := range cases {
		cfg := componentShapesBase(func(c *ComponentConfig) {
			c.TagGrammar = &TagGrammarConfig{Prefix: strptr(prefix)}
		})
		errs := Validate(cfg)
		assert.NotEmpty(t, errs, "component tag prefix %q must be rejected", prefix)
	}
}

func TestValidate_GitIdentity_RejectsShellDoubleQuoteBreakers(t *testing.T) {
	hostile := []string{`x"; id; echo "`, "x$y", "x`y`", `x\y`, "x\ny"}
	for _, v := range hostile {
		cfg := emittedShapesBase()
		cfg.Git = &GitConfig{Mode: GitModeCustom, UserName: v, UserEmail: "bot@example.com"}
		errs := Validate(cfg)
		assert.NotEmpty(t, errs, "git.user_name %q must be rejected", v)
		assert.Contains(t, strings.Join(errs, "\n"), "git.user_name")

		cfg = emittedShapesBase()
		cfg.Git = &GitConfig{Mode: GitModeCustom, UserName: "Release Bot", UserEmail: v}
		errs = Validate(cfg)
		assert.NotEmpty(t, errs, "git.user_email %q must be rejected", v)
	}
}

func TestValidate_GitIdentity_ApostropheAndBracketsAccepted(t *testing.T) {
	// Apostrophes are safe inside the emitted double quotes; brackets appear in
	// the default bot identity.
	cfg := emittedShapesBase()
	cfg.Git = &GitConfig{Mode: GitModeCustom, UserName: "O'Brien [bot]", UserEmail: "o'brien@example.com"}
	assert.Empty(t, Validate(cfg))
}

func TestValidate_GPGSecretNames_RejectNonSecretShapes(t *testing.T) {
	hostile := []string{"BAD-NAME", "1KEY", "A B", "A }} ${{ B", "k\ny"}
	for _, v := range hostile {
		cfg := emittedShapesBase()
		cfg.Git = &GitConfig{GPGKeyID: v, GPGKeySecret: "GPG_PRIVATE_KEY"}
		errs := Validate(cfg)
		assert.NotEmpty(t, errs, "git.gpg_key_id %q must be rejected", v)
		assert.Contains(t, strings.Join(errs, "\n"), "git.gpg_key_id")

		cfg = emittedShapesBase()
		cfg.Git = &GitConfig{GPGKeyID: "GPG_KEY_ID", GPGKeySecret: v}
		errs = Validate(cfg)
		assert.NotEmpty(t, errs, "git.gpg_key_secret %q must be rejected", v)
		assert.Contains(t, strings.Join(errs, "\n"), "git.gpg_key_secret")
	}

	cfg := emittedShapesBase()
	cfg.Git = &GitConfig{GPGKeyID: "GPG_KEY_ID", GPGKeySecret: "GPG_PRIVATE_KEY"}
	assert.Empty(t, Validate(cfg))
}

func TestValidate_EnvironmentURL_ShapeEnforced(t *testing.T) {
	hostile := []string{
		"ftp://example.com",          // not http(s)
		"https://x.com/a'b",          // closes the emitted single quote
		"https://x.com/a b",          // whitespace
		"https://x.com/a\nb",         // newline
		"javascript:alert(1)",        // not http(s)
	}
	for _, u := range hostile {
		cfg := emittedShapesBase()
		cfg.Environments = []EnvironmentEntry{
			{Name: "dev"},
			{Name: "prod", EnvironmentConfig: EnvironmentConfig{EnvironmentURL: u}},
		}
		errs := Validate(cfg)
		assert.NotEmpty(t, errs, "environment_url %q must be rejected", u)
		assert.Contains(t, strings.Join(errs, "\n"), "environment_url")
	}

	// $ is realistic in a URL query (OData-style $filter/$top) and must be
	// accepted; the generator single-quotes the value so the shell never
	// expands it.
	cfg := emittedShapesBase()
	cfg.Environments = []EnvironmentEntry{
		{Name: "dev"},
		{Name: "prod", EnvironmentConfig: EnvironmentConfig{EnvironmentURL: "https://app.example.com/?$top=1&$filter=x"}},
	}
	assert.Empty(t, Validate(cfg))
}

func TestValidate_NotifyFields_ShapeEnforced(t *testing.T) {
	base := func() *TrunkConfig {
		cfg := emittedShapesBase()
		cfg.Notify = &NotifyConfig{Repo: "org/primary"}
		return cfg
	}

	cfg := base()
	cfg.Notify.Repo = "orgonly"
	errs := Validate(cfg)
	assert.NotEmpty(t, errs, "notify.repo without owner/name must be rejected (the notify step is silently dropped otherwise)")
	assert.Contains(t, strings.Join(errs, "\n"), "notify.repo")

	for _, v := range []string{"x'y", "x\ny", `x\y`} {
		cfg = base()
		cfg.Notify.Workflow = v
		assert.NotEmpty(t, Validate(cfg), "notify.workflow %q must be rejected", v)

		cfg = base()
		cfg.Notify.DeployName = v
		assert.NotEmpty(t, Validate(cfg), "notify.deploy_name %q must be rejected", v)

		cfg = base()
		cfg.Notify.Environment = v
		assert.NotEmpty(t, Validate(cfg), "notify.environment %q must be rejected", v)
	}

	cfg = base()
	cfg.Notify.Workflow = "external-update.yaml"
	cfg.Notify.DeployName = "backend"
	cfg.Notify.Environment = "dev"
	assert.Empty(t, Validate(cfg))
}

func TestValidate_TrunkBranch_ShapeEnforced(t *testing.T) {
	for _, v := range []string{"main'x", "a b", "x\ny", "x$y", "-main", "a,b]"} {
		cfg := emittedShapesBase()
		cfg.TrunkBranch = v
		errs := Validate(cfg)
		assert.NotEmpty(t, errs, "trunk_branch %q must be rejected", v)
		assert.Contains(t, strings.Join(errs, "\n"), "trunk_branch")
	}
	for _, v := range []string{"main", "trunk", "release/2026", "dev.main_1"} {
		cfg := emittedShapesBase()
		cfg.TrunkBranch = v
		assert.Empty(t, Validate(cfg), "trunk_branch %q is legitimate", v)
	}
}

func TestValidate_ManifestFileAndKey_ShapeEnforced(t *testing.T) {
	for _, v := range []string{`x"y`, "x$y", "x`y", "x\ny"} {
		cfg := emittedShapesBase()
		cfg.ManifestFile = v
		assert.NotEmpty(t, Validate(cfg), "manifest_file %q must be rejected", v)
	}
	for _, v := range []string{"ci key", "ci:x", "ci\nx", "ci'"} {
		cfg := emittedShapesBase()
		cfg.ManifestKey = v
		assert.NotEmpty(t, Validate(cfg), "manifest_key %q must be rejected", v)
	}
	cfg := emittedShapesBase()
	cfg.ManifestFile = ".github/manifest.yaml"
	cfg.ManifestKey = "ci"
	assert.Empty(t, Validate(cfg))
}

func TestValidate_TokenExpressions_ShapeEnforced(t *testing.T) {
	hostile := []string{"SEC\nRET", "x: y", "${{ secrets.A }} #x"}
	for _, v := range hostile {
		cfg := emittedShapesBase()
		cfg.ReleaseToken = v
		assert.NotEmpty(t, Validate(cfg), "release_token %q must be rejected", v)

		cfg = emittedShapesBase()
		cfg.StateToken = v
		assert.NotEmpty(t, Validate(cfg), "state_token %q must be rejected", v)

		cfg = emittedShapesBase()
		cfg.Notify = &NotifyConfig{Repo: "org/primary", Token: v}
		assert.NotEmpty(t, Validate(cfg), "notify.token %q must be rejected", v)
	}
	cfg := emittedShapesBase()
	cfg.ReleaseToken = "${{ secrets.MY_TOKEN }}"
	cfg.StateToken = "MY_STATE_TOKEN"
	assert.Empty(t, Validate(cfg))
}

func TestValidate_TriggerPatterns_RejectNewlines(t *testing.T) {
	cfg := emittedShapesBase()
	cfg.Triggers = []string{"src/**", "docs/**\n- pwned"}
	assert.NotEmpty(t, Validate(cfg), "a trigger pattern with a newline must be rejected")

	cfg = emittedShapesBase()
	cfg.Builds = []BuildConfig{{Name: "app", Workflow: "build.yaml", Triggers: []string{"a\nb"}}}
	assert.NotEmpty(t, Validate(cfg), "a build trigger pattern with a newline must be rejected")

	cfg = componentShapesBase(func(c *ComponentConfig) {
		c.ExtraPaths = []string{"shared/**\n- pwned"}
	})
	assert.NotEmpty(t, Validate(cfg), "a component extra_paths pattern with a newline must be rejected")

	cfg = componentShapesBase(nil)
	cfg.SharedPaths = []string{"go.mod\nx"}
	assert.NotEmpty(t, Validate(cfg), "a shared_paths pattern with a newline must be rejected")

	// Apostrophes in a glob are unusual but harmless once the emitter
	// single-quote-escapes them.
	cfg = emittedShapesBase()
	cfg.Triggers = []string{"src/it's/**"}
	assert.Empty(t, Validate(cfg))
}

// TestValidate_ResolvedComponents_SectionRulesApply pins the systemic scope
// fix: every rule that guards the top-level manifest also guards each
// component's resolved (effective) configuration, so a component-scope value
// can never reach the generator unvalidated.
func TestValidate_ResolvedComponents_SectionRulesApply(t *testing.T) {
	cfg := componentShapesBase(func(c *ComponentConfig) {
		c.Builds = []BuildConfig{{Name: "bad name", Workflow: "build.yaml"}}
	})
	errs := Validate(cfg)
	joined := strings.Join(errs, "\n")
	require.NotEmpty(t, errs, "a component build name outside the job-ID grammar must be rejected")
	assert.Contains(t, joined, "components.api")
	assert.Contains(t, joined, "bad name")

	cfg = componentShapesBase(func(c *ComponentConfig) {
		c.Environments = []EnvironmentEntry{{Name: "d ev"}}
	})
	assert.NotEmpty(t, Validate(cfg), "a component environment name outside the job-ID grammar must be rejected")

	cfg = componentShapesBase(func(c *ComponentConfig) {
		c.DispatchInputs = map[string]DispatchInput{
			"mode": {Type: DispatchInputTypeChoice, Options: []string{"a b"}},
		}
	})
	assert.NotEmpty(t, Validate(cfg), "a component choice option outside the option grammar must be rejected")

	cfg = componentShapesBase(func(c *ComponentConfig) {
		c.Notify = &NotifyConfig{Repo: "org/primary", DeployName: "x'y"}
	})
	assert.NotEmpty(t, Validate(cfg), "a component notify.deploy_name must obey the emitted-scalar rules")
}

// TestValidate_ResolvedComponents_InheritedErrorsNotDuplicated asserts that an
// error on a shared top-level value is reported once, not re-reported for
// every component that inherits it.
func TestValidate_ResolvedComponents_InheritedErrorsNotDuplicated(t *testing.T) {
	cfg := emittedShapesBase()
	cfg.Git = &GitConfig{Mode: GitModeCustom, UserName: `x"y`, UserEmail: "bot@example.com"}
	cfg.Components = map[string]ComponentConfig{
		"api": {Path: "services/api", TagGrammar: &TagGrammarConfig{Prefix: strptr("api-v")}},
		"web": {Path: "services/web", TagGrammar: &TagGrammarConfig{Prefix: strptr("web-v")}},
	}
	errs := Validate(cfg)
	count := 0
	for _, e := range errs {
		if strings.Contains(e, "git.user_name") {
			count++
		}
	}
	assert.Equal(t, 1, count, "an inherited top-level error must be reported once, got: %v", errs)
}

// TestValidate_ResolvedComponents_ValidBaseStaysValid guards against the
// resolved-component pass introducing false rejections.
func TestValidate_ResolvedComponents_ValidBaseStaysValid(t *testing.T) {
	cfg := componentShapesBase(func(c *ComponentConfig) {
		c.Builds = []BuildConfig{{Name: "app", Workflow: "build.yaml"}}
		c.Environments = EnvNames("dev", "prod")
	})
	assert.Empty(t, Validate(cfg))
}
