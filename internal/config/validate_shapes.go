package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Shared shape validators for manifest values that are spliced into emitted
// GitHub Actions contexts (YAML keys and scalars, shell strings, github-script
// JS, cron entries, argv, secret-name expressions). Each helper is named for
// the sink context it guards, so a new emitted field picks the helper matching
// its sink instead of growing an ad-hoc rule. The emitted-field guard test in
// internal/generate (TestEmittedFieldRegistry_*) forces every new manifest
// field to be classified against one of these shapes or explicitly recorded as
// not emitted.
//
// The philosophy matches the rest of validate_v1.go: reject at the manifest
// boundary rather than escape at emit time, except where a value has a
// legitimate need for the risky character (then the emitter quotes and the
// validator only rejects what quoting cannot carry, such as a newline).

// cronFieldRe bounds a single cron field to the grammar GitHub Actions
// accepts: digits, `*`, steps (`/`), ranges (`-`), lists (`,`), and the
// JAN-DEC / SUN-SAT month and weekday names.
var cronFieldRe = regexp.MustCompile(`^[A-Za-z0-9*,/-]+$`)

// validateCronExpr checks a schedule cron expression: single-line, exactly
// five whitespace-separated fields, each within the GHA cron charset. The
// expression is emitted inside a single-quoted YAML scalar under on.schedule,
// so an unchecked value could otherwise break or restructure the emitted
// document.
func validateCronExpr(field, expr string) []string {
	if strings.TrimSpace(expr) == "" {
		return []string{fmt.Sprintf("%s must not be empty", field)}
	}
	if strings.ContainsAny(expr, "\r\n") {
		return []string{fmt.Sprintf("%s %q must be a single line", field, expr)}
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return []string{fmt.Sprintf(
			"%s %q must be a five-field cron expression (minute hour day-of-month month day-of-week), got %d fields",
			field, expr, len(fields))}
	}
	for _, f := range fields {
		if !cronFieldRe.MatchString(f) {
			return []string{fmt.Sprintf(
				"%s %q field %q must contain only digits, letters, '*', ',', '-', and '/'", field, expr, f)}
		}
	}
	return nil
}

// validateEventTypeNames checks a list of GitHub event-type names
// (repository_dispatch and workflow_run types) under the given prefix. Each
// entry is emitted verbatim as an unquoted YAML sequence item, so the accepted
// set is constrained to characters that need no escaping (see
// repositoryDispatchTypeRe).
func validateEventTypeNames(prefix string, types []string) []string {
	var errs []string
	for _, t := range types {
		if strings.TrimSpace(t) == "" {
			errs = append(errs, fmt.Sprintf("%s must not contain a blank event type", prefix))
			continue
		}
		if !repositoryDispatchTypeRe.MatchString(t) {
			errs = append(errs, fmt.Sprintf(
				"%s entry %q must contain only letters, digits, dots, hyphens, and underscores", prefix, t))
		}
	}
	return errs
}

// validateSingleLine rejects a value carrying a newline or carriage return.
// It guards free scalars whose emitters quote them as single-line YAML
// single-quoted scalars (yamlSingleQuote), whose only unescapable content is a
// line break.
func validateSingleLine(field, value string) []string {
	if strings.ContainsAny(value, "\r\n") {
		return []string{fmt.Sprintf("%s %q must be a single line", field, value)}
	}
	return nil
}

// validateShellDoubleQuoted rejects characters that stay live inside a
// double-quoted POSIX shell string: double quotes end the string, `$` and
// backticks expand, and a backslash escapes. Apostrophes are safe here and
// stay allowed (they are the one special character real names carry).
func validateShellDoubleQuoted(field, value string) []string {
	if errs := validateSingleLine(field, value); len(errs) > 0 {
		return errs
	}
	if strings.ContainsAny(value, "\"$`\\") {
		return []string{fmt.Sprintf(
			"%s %q must not contain double quotes, '$', backticks, or backslashes; the value is emitted inside a double-quoted shell string", field, value)}
	}
	return nil
}

// validateJSSingleQuoted rejects characters that break out of a single-quoted
// github-script JavaScript string literal: single quotes end the literal and a
// backslash escapes.
func validateJSSingleQuoted(field, value string) []string {
	if errs := validateSingleLine(field, value); len(errs) > 0 {
		return errs
	}
	if strings.ContainsAny(value, `'\`) {
		return []string{fmt.Sprintf(
			"%s %q must not contain single quotes or backslashes; the value is emitted inside a single-quoted script literal", field, value)}
	}
	return nil
}

// validateGHASecretName checks a value that is spliced into a
// ${{ secrets.<name> }} expression. Anything outside GitHub's secret-name
// grammar (letters, digits, underscores, not starting with a digit) could
// break or extend the emitted expression.
func validateGHASecretName(field, name string) []string {
	if name == "" || !safeSecretName(name) {
		return []string{fmt.Sprintf(
			"%s %q must be a valid GitHub Actions secret name (letters, digits, underscores; not starting with a digit)", field, name)}
	}
	return nil
}

// validateTagPrefixShape checks a tag_grammar.prefix value (top-level or
// per-component): the tagGrammarAllowedChars charset, plus a positional rule
// the charset alone cannot express. The prefix reaches `git tag -l <prefix>*`
// argv, where a leading hyphen parses as a flag and fails every tag lookup
// with exit 129, so it is rejected up front.
func validateTagPrefixShape(field, prefix string) []string {
	var errs []string
	if tagGrammarUnsafeChar(prefix) {
		errs = append(errs, fmt.Sprintf(
			"%s %q must contain only letters, digits, '.', '_', and '-'", field, prefix))
	}
	if strings.HasPrefix(prefix, "-") {
		errs = append(errs, fmt.Sprintf(
			"%s %q must not begin with a hyphen; the prefix is passed to git tag lookups where a leading hyphen parses as a flag", field, prefix))
	}
	return errs
}

// gitRefSafeRe bounds a branch name to characters safe in every context the
// generator splices it into: a YAML flow sequence (branches: [<name>]), a
// single-quoted github-script literal, a double-quoted shell string, and git
// argv.
var gitRefSafeRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// validateGitRefName checks a branch name field against gitRefSafeRe and
// rejects a leading hyphen (argv flag confusion). Empty values are the
// caller's concern (requiredness differs per field).
func validateGitRefName(field, name string) []string {
	if name == "" {
		return nil
	}
	if !gitRefSafeRe.MatchString(name) {
		return []string{fmt.Sprintf(
			"%s %q must contain only letters, digits, dots, slashes, hyphens, and underscores", field, name)}
	}
	if strings.HasPrefix(name, "-") {
		return []string{fmt.Sprintf("%s %q must not begin with a hyphen", field, name)}
	}
	return nil
}

// repoSlugRe bounds an owner/repo reference to GitHub's naming grammar. The
// two segments are spliced into single-quoted github-script literals.
var repoSlugRe = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// manifestKeyRe bounds the manifest_key value: it is emitted as a raw YAML
// scalar (MANIFEST_KEY: <key>) and inside double-quoted shell strings.
var manifestKeyRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateTokenExpressionShape checks a token field (release_token,
// state_token, notify.token). The normalized ${{ ... }} expression is spliced
// into unquoted YAML scalar positions (token:, GH_TOKEN:), where a newline, a
// colon, or a comment marker would restructure the document.
func validateTokenExpressionShape(field, value string) []string {
	if errs := validateSingleLine(field, value); len(errs) > 0 {
		return errs
	}
	if strings.ContainsAny(value, ":#") {
		return []string{fmt.Sprintf(
			"%s %q must not contain ':' or '#'; the value is emitted into an unquoted YAML scalar", field, value)}
	}
	return nil
}

// validateEnvironmentURL checks an environment_url value: it must be an
// http(s) URL (mirroring telemetry.webhook.url) and must not carry characters
// that a single-quoted shell assignment cannot hold (a single quote) or that a
// URL never legitimately contains raw (whitespace, control characters). `$`
// stays allowed: it is realistic in query strings and the emitter
// single-quotes the value so the shell never expands it.
func validateEnvironmentURL(field, url string) []string {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return []string{fmt.Sprintf("%s %q must be an http(s) URL", field, url)}
	}
	if strings.Contains(url, "'") {
		return []string{fmt.Sprintf("%s %q must not contain single quotes", field, url)}
	}
	for _, r := range url {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return []string{fmt.Sprintf("%s %q must not contain whitespace or control characters", field, url)}
		}
	}
	return nil
}

// validatePathPatterns rejects trigger/paths glob patterns that cannot be
// carried by the single-quoted YAML sequence items the generator emits under
// the push paths filter: blank entries and entries with line breaks.
func validatePathPatterns(prefix string, patterns []string) []string {
	var errs []string
	for i, p := range patterns {
		field := fmt.Sprintf("%s[%d]", prefix, i)
		if strings.TrimSpace(p) == "" {
			errs = append(errs, field+" must not be blank")
			continue
		}
		errs = append(errs, validateSingleLine(field, p)...)
	}
	return errs
}

// ghaIdentifierRe bounds a name that is dereferenced inside a ${{ }}
// expression or emitted as a with:/outputs: YAML key (workflow input names,
// callback output names): a letter or underscore, then letters, digits,
// hyphens, underscores. Mirrors matrixDimensionKeyRe, which guards the same
// sink shape for matrix keys.
var ghaIdentifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// cliVersionRe bounds cli_version: it is emitted as the ref after
// "setup-cli@" in generated uses: lines (and as a trailing comment under
// pin_mode: sha), so it is constrained to ref-safe characters.
var cliVersionRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateYAMLPlainScalar rejects content that restructures an unquoted YAML
// scalar position: line breaks, ": ", and "#" (comment). Used for values the
// generator deliberately emits unquoted so ${{ }} expressions survive
// verbatim (concurrency group, token expressions share the rule via
// validateTokenExpressionShape).
func validateYAMLPlainScalar(field, value string) []string {
	if errs := validateSingleLine(field, value); len(errs) > 0 {
		return errs
	}
	if strings.ContainsAny(value, ":#") {
		return []string{fmt.Sprintf(
			"%s %q must not contain ':' or '#'; the value is emitted into an unquoted YAML scalar", field, value)}
	}
	return nil
}

// validateExternalWorkflowPath checks an external callback workflow path: a
// local .github/... path or an org/repo/.github/...(@ref) reference, both
// spliced raw into a generated uses: line. The charset mirrors
// validateLocalCallbackWorkflowPath; the cross-repo shape additionally allows
// the org/repo prefix without requiring an inline @ref (the external repo's
// ref is appended at emit time).
func validateExternalWorkflowPath(field, workflow string) []string {
	if workflow == "" {
		return nil
	}
	if containsUnsafeChar(workflow) {
		return []string{fmt.Sprintf("%s: workflow %q contains unsafe characters", field, workflow)}
	}
	if strings.Contains(workflow, "@") {
		if !crossRepoCallbackRe.MatchString(workflow) {
			return []string{fmt.Sprintf("%s: workflow %q must be a path@ref reference", field, workflow)}
		}
		return nil
	}
	if !localCallbackPathRe.MatchString(workflow) {
		return []string{fmt.Sprintf("%s: workflow %q contains unsafe characters", field, workflow)}
	}
	return nil
}

// validateCallbackInputShapes checks a callback's operator-authored inputs
// map. Keys are emitted as with: YAML keys (gated on the callback declaring a
// matching workflow input, whose grammar GitHub itself enforces); values, when
// strings, are emitted as single-quoted with: scalars in the orchestrate
// workflow, so only a line break cannot be carried. env_inputs values travel
// exclusively through the env-routed JSON matrices (newline-safe) and are not
// checked here; their env-name keys are validated against the declared
// environments by the caller.
func validateCallbackInputShapes(prefix string, inputs map[string]interface{}) []string {
	var errs []string
	keys := make([]string, 0, len(inputs))
	for k := range inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !ghaIdentifierRe.MatchString(k) {
			errs = append(errs, fmt.Sprintf(
				"%s.inputs key %q must start with a letter or underscore and contain only letters, digits, hyphens, and underscores", prefix, k))
		}
		if s, ok := inputs[k].(string); ok {
			errs = append(errs, validateSingleLine(fmt.Sprintf("%s.inputs.%s", prefix, k), s)...)
		}
	}
	return errs
}

// validatePassthroughArtifact checks an artifact_upload block. The upload glob
// is spliced raw into upload/download-artifact with: path lines, so it is
// bounded to the same grammar as release-artifact paths. Download entries name
// upstream builds and are spliced into artifact names, step names, and derived
// job IDs, so they obey the job-ID grammar.
func validatePassthroughArtifact(prefix string, pa *PassthroughArtifact) []string {
	if pa == nil {
		return nil
	}
	var errs []string
	if pa.Upload != "" {
		switch {
		case !artifactPathRe.MatchString(pa.Upload):
			errs = append(errs, fmt.Sprintf(
				"%s.artifact_upload.upload %q must contain only letters, digits, dots, slashes, hyphens, underscores, and glob characters", prefix, pa.Upload))
		case strings.HasPrefix(pa.Upload, "-"):
			errs = append(errs, fmt.Sprintf("%s.artifact_upload.upload %q must not start with a hyphen", prefix, pa.Upload))
		}
	}
	for i, d := range pa.Downloads {
		if d == "" || !jobIDSafeNameRe.MatchString(d) {
			errs = append(errs, fmt.Sprintf(
				"%s.artifact_upload.downloads[%d] %q must contain only letters, digits, hyphens, and underscores", prefix, i, d))
		}
	}
	return errs
}

// validateNotifyShapes checks the notify block's emitted scalars. repo is
// split into owner/name and both halves land in single-quoted github-script
// literals; a value without a slash makes the generator silently drop the
// notify step, so the slug shape is enforced here. workflow, deploy_name, and
// environment land in the same script literals. token joins the shared token
// expression rule.
func validateNotifyShapes(n *NotifyConfig) []string {
	if n == nil {
		return nil
	}
	var errs []string
	if n.Repo != "" && !repoSlugRe.MatchString(n.Repo) {
		errs = append(errs, fmt.Sprintf(
			"notify.repo %q must be an owner/repo slug (letters, digits, dots, hyphens, underscores)", n.Repo))
	}
	errs = append(errs, validateJSSingleQuoted("notify.workflow", n.Workflow)...)
	errs = append(errs, validateJSSingleQuoted("notify.deploy_name", n.DeployName)...)
	errs = append(errs, validateJSSingleQuoted("notify.environment", n.Environment)...)
	errs = append(errs, validateTokenExpressionShape("notify.token", n.Token)...)
	return errs
}

// validateEmittedScalars is the config-level boundary check for free-scalar
// manifest fields that the generator splices into emitted workflows. Every
// field checked here reaches a shell, JS, YAML, or expression sink; the shape
// applied is the one its sink can carry safely.
func validateEmittedScalars(cfg *TrunkConfig) []string {
	var errs []string

	// trunk_branch is required, matching the published schema. It is the sole
	// source of the orchestrate push allow-list, so an unset value used to emit
	// "branches: []" and produce a workflow that could never fire on a trunk
	// push. There is no safe default to infer: guessing "main" for a repo whose
	// trunk is "master" rebuilds the same dead workflow silently. Ask instead.
	if cfg.TrunkBranch == "" {
		errs = append(errs, "trunk_branch is required (for example \"main\"): "+
			"it sets the branch the orchestrate workflow runs on")
	} else {
		errs = append(errs, validateGitRefName("trunk_branch", cfg.TrunkBranch)...)
	}

	if cfg.ManifestFile != "" {
		errs = append(errs, validateShellDoubleQuoted("manifest_file", cfg.ManifestFile)...)
	}
	if cfg.ManifestKey != "" && !manifestKeyRe.MatchString(cfg.ManifestKey) {
		errs = append(errs, fmt.Sprintf(
			"manifest_key %q must contain only letters, digits, dots, hyphens, and underscores", cfg.ManifestKey))
	}

	if cfg.Git != nil {
		if cfg.Git.UserName != "" {
			errs = append(errs, validateShellDoubleQuoted("git.user_name", cfg.Git.UserName)...)
		}
		if cfg.Git.UserEmail != "" {
			errs = append(errs, validateShellDoubleQuoted("git.user_email", cfg.Git.UserEmail)...)
		}
		if cfg.Git.GPGKeyID != "" {
			errs = append(errs, validateGHASecretName("git.gpg_key_id", cfg.Git.GPGKeyID)...)
		}
		if cfg.Git.GPGKeySecret != "" {
			errs = append(errs, validateGHASecretName("git.gpg_key_secret", cfg.Git.GPGKeySecret)...)
		}
	}

	errs = append(errs, validateNotifyShapes(cfg.Notify)...)

	errs = append(errs, validateTokenExpressionShape("release_token", cfg.ReleaseToken)...)
	errs = append(errs, validateTokenExpressionShape("state_token", cfg.StateToken)...)

	errs = append(errs, validatePathPatterns("triggers", cfg.Triggers)...)
	errs = append(errs, validatePathPatterns("shared_paths", cfg.SharedPaths)...)

	// cli_version is emitted as the setup-cli@<ref> ref in every generated
	// workflow.
	if cfg.CLIVersion != "" && !cliVersionRe.MatchString(cfg.CLIVersion) {
		errs = append(errs, fmt.Sprintf(
			"cli_version %q must contain only letters, digits, dots, hyphens, and underscores", cfg.CLIVersion))
	}

	// The orchestrate concurrency group is emitted as an unquoted YAML scalar
	// so ${{ }} expressions survive verbatim.
	if cfg.Concurrency != nil && cfg.Concurrency.Group != "" {
		errs = append(errs, validateYAMLPlainScalar("concurrency.group", cfg.Concurrency.Group)...)
	}

	// Callback workflow references outside the build/deploy/validate loops:
	// each is spliced into a uses: line or a `gh workflow run` invocation.
	if cfg.Changelog != nil {
		errs = append(errs, validateLocalCallbackWorkflowPath("changelog.workflow", cfg.Changelog.Workflow)...)
	}
	if cfg.Publish != nil {
		errs = append(errs, validateLocalCallbackWorkflowPath("publish.workflow", cfg.Publish.Workflow)...)
	}
	if cfg.ReleaseBuild != nil {
		errs = append(errs, validateLocalCallbackWorkflowPath("release_build.workflow", cfg.ReleaseBuild.Workflow)...)
	}

	// External repo refs and workflow paths are spliced into uses: lines
	// (workflow@ref) in the promote and external-update workflows.
	for i, ext := range cfg.External {
		if ext.Ref != "" {
			errs = append(errs, validateGitRefName(fmt.Sprintf("external[%d].ref", i), ext.Ref)...)
		}
		for j, d := range ext.Deploys {
			p := fmt.Sprintf("external[%d].deploys[%d]", i, j)
			errs = append(errs, validateExternalWorkflowPath(p, d.Workflow)...)
			if d.OnUpdate != nil && d.OnUpdate.Deploy != nil {
				errs = append(errs, validateExternalWorkflowPath(p+".on_update.deploy", d.OnUpdate.Deploy.Workflow)...)
			}
			errs = append(errs, validatePathPatterns(p+".triggers", d.Triggers)...)
		}
	}

	return errs
}
