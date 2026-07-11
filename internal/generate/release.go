package generate

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// ReleaseGenerator handles release workflow generation for single-environment projects
type ReleaseGenerator struct {
	config  *config.TrunkConfig
	baseDir string
}

// NewReleaseGenerator creates a new release workflow generator
func NewReleaseGenerator(cfg *config.TrunkConfig, baseDir string) *ReleaseGenerator {
	return &ReleaseGenerator{
		config:  cfg,
		baseDir: baseDir,
	}
}

// getCLIRef returns the Git ref for the cascade self-action. The default
// (cli_version unset or "latest") resolves to config.DefaultCLIVersion, an
// immutable release tag, so consumers never run an unpinned mutable ref.
// Supported values:
//   - unset / "latest" → config.DefaultCLIVersion (immutable, pinned default)
//   - "beta" → "master" branch (explicit opt-in, bleeding edge, may be unstable)
//   - "vX.Y.Z" → that specific version tag
func (g *ReleaseGenerator) getCLIRef() string {
	return cliSetupRef(g.config)
}

// getReleaseTokenRef returns the token expression for release operations.
// Users configure the full expression via release_token config option.
func (g *ReleaseGenerator) getReleaseTokenRef() string {
	return resolveReleaseTokenRef(g.config)
}

// getStateTokenRef returns the token expression used to write manifest state to
// the trunk branch. Users configure the full expression via the state_token
// config option; it defaults to the release token expression so existing
// manifests keep using a single token.
func (g *ReleaseGenerator) getStateTokenRef() string {
	return resolveStateTokenRef(g.config)
}

// getManifestFilePath returns the manifest file path for use in generated scripts.
// Converts absolute paths to repo-relative paths since workflows run in checked out repos.
func (g *ReleaseGenerator) getManifestFilePath() string {
	manifestPath := g.config.GetManifestFile()

	// If it's already relative, return as-is
	if !filepath.IsAbs(manifestPath) {
		return manifestPath
	}

	// If baseDir is set and manifestPath starts with it, make relative
	if g.baseDir != "" {
		if rel, err := filepath.Rel(g.baseDir, manifestPath); err == nil {
			return rel
		}
	}

	// Fallback: return default relative path
	return ".github/manifest.yaml"
}

// getManifestKey returns the manifest key for nested access
func (g *ReleaseGenerator) getManifestKey() string {
	return g.config.GetManifestKey()
}

// getActionPath returns the path to the manage-release action
func (g *ReleaseGenerator) getActionPath() string {
	return fmt.Sprintf("./.github/actions/%s", g.config.GetActionFolder())
}

// Generate creates the release workflow content for single-environment projects
func (g *ReleaseGenerator) Generate() (string, error) {
	var sb strings.Builder

	g.writeHeader(&sb)
	g.writeWorkflowTriggers(&sb)
	g.writeConcurrency(&sb)
	g.writeJobs(&sb)

	return sb.String(), nil
}

func (g *ReleaseGenerator) writeHeader(sb *strings.Builder) {
	sb.WriteString(GeneratedFileMarker + "\n")
	fmt.Fprintf(sb, "# Regenerate with: cascade generate-workflow --config %s\n", g.getManifestFilePath())
	sb.WriteString("#\n")

	// Document single environment
	sb.WriteString("# Environment: ")
	if len(g.config.Environments) > 0 {
		sb.WriteString(g.config.Environments[0])
	}
	sb.WriteString("\n#\n")

	// Document release actions
	sb.WriteString("# Release actions:\n")
	sb.WriteString("#   create-draft - Creates/updates a draft release with changelog\n")
	sb.WriteString("#   prerelease   - Publishes as a pre-release (for testing)\n")
	sb.WriteString("#   release      - Publishes as a full release\n")
	sb.WriteString("#\n")

	// Document breaking change gate
	sb.WriteString("# Breaking changes:\n")
	sb.WriteString("#   If changelog contains breaking changes, release\n")
	sb.WriteString("#   will fail unless 'allow_breaking_changes' is checked.\n")
	sb.WriteString("\n")
}

func (g *ReleaseGenerator) writeWorkflowTriggers(sb *strings.Builder) {
	sb.WriteString("name: Release\n\n")
	sb.WriteString("on:\n")
	sb.WriteString("  workflow_dispatch:\n")
	sb.WriteString("    inputs:\n")

	// Release action input
	sb.WriteString("      release_action:\n")
	sb.WriteString("        description: 'Release action to perform'\n")
	sb.WriteString("        type: choice\n")
	sb.WriteString("        required: true\n")
	sb.WriteString("        options:\n")
	sb.WriteString("          - create-draft\n")
	sb.WriteString("          - prerelease\n")
	sb.WriteString("          - release\n")
	sb.WriteString("        default: create-draft\n")

	// Breaking changes checkbox
	sb.WriteString("      allow_breaking_changes:\n")
	sb.WriteString("        description: 'Required if releasing breaking changes'\n")
	sb.WriteString("        type: boolean\n")
	sb.WriteString("        default: false\n")

	// Dry run option
	sb.WriteString("      dry_run:\n")
	sb.WriteString("        description: 'Dry run mode'\n")
	sb.WriteString("        type: boolean\n")
	sb.WriteString("        default: false\n")
	sb.WriteString("\n")
}

// The release workflow tags, generates the changelog, and publishes a GitHub
// release; it performs no environment deploy, so there is no deploy to
// auto-roll-back (unlike promote/hotfix). Auto-rollback parity is intentionally
// not emitted here.
func (g *ReleaseGenerator) writeJobs(sb *strings.Builder) {
	sb.WriteString("jobs:\n")
	g.writePreflightJob(sb)
	g.writeReleaseJob(sb)
	g.writeFinalizeJob(sb)
}

func (g *ReleaseGenerator) writePreflightJob(sb *strings.Builder) {
	env := ""
	if len(g.config.Environments) > 0 {
		env = g.config.Environments[0]
	}

	sb.WriteString("  preflight:\n")
	sb.WriteString("    name: Pre-flight Check\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    outputs:\n")
	sb.WriteString("      has_breaking: ${{ steps.check.outputs.has_breaking }}\n")
	sb.WriteString("      can_proceed: ${{ steps.check.outputs.can_proceed }}\n")
	sb.WriteString("      source_sha: ${{ steps.validate.outputs.source_sha }}\n")
	sb.WriteString("      source_version: ${{ steps.validate.outputs.source_version }}\n")
	sb.WriteString("      semver_tag: ${{ steps.semver.outputs.semver_tag }}\n")
	sb.WriteString("    steps:\n")
	writeMintSteps(sb, g.config, "      ", seamRelease)
	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          fetch-depth: 0\n")

	// Validate environment state
	sb.WriteString("      - name: Validate Environment State\n")
	sb.WriteString("        id: validate\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          # Colorized logging helpers\n")
	sb.WriteString("          log_info() { echo -e \"\\033[36m[INFO]\\033[0m $1\"; }\n")
	sb.WriteString("          log_success() { echo -e \"\\033[32m[OK]\\033[0m $1\"; }\n")
	sb.WriteString("          log_warn() { echo -e \"\\033[33m[WARN]\\033[0m $1\"; }\n")
	sb.WriteString("          log_error() { echo -e \"\\033[31m[ERROR]\\033[0m $1\"; }\n")
	sb.WriteString("          \n")
	fmt.Fprintf(sb, "          log_info \"Validating %s environment state\"\n", env)
	sb.WriteString("          \n")
	fmt.Fprintf(sb, "          MANIFEST_FILE=\"%s\"\n", g.getManifestFilePath())
	fmt.Fprintf(sb, "          MANIFEST_KEY=\"%s\"\n", g.getManifestKey())
	sb.WriteString("          if [[ ! -f \"$MANIFEST_FILE\" ]]; then\n")
	sb.WriteString("            log_error \"$MANIFEST_FILE not found\"\n")
	sb.WriteString("            exit 1\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          log_success \"Found $MANIFEST_FILE\"\n")
	sb.WriteString("          \n")
	fmt.Fprintf(sb, "          SOURCE_SHA=$(yq eval \".$MANIFEST_KEY.state.%s.sha // \\\"\\\"\" \"$MANIFEST_FILE\")\n", env)
	fmt.Fprintf(sb, "          SOURCE_VERSION=$(yq eval \".$MANIFEST_KEY.state.%s.version // \\\"\\\"\" \"$MANIFEST_FILE\")\n", env)
	sb.WriteString("          \n")
	sb.WriteString("          if [[ -z \"$SOURCE_SHA\" || \"$SOURCE_SHA\" == \"null\" ]]; then\n")
	fmt.Fprintf(sb, "            log_error \"No SHA found in state for %s environment\"\n", env)
	sb.WriteString("            log_info \"Run the build workflow first to create a deployment\"\n")
	sb.WriteString("            exit 1\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          log_success \"Source SHA: ${SOURCE_SHA:0:7}\"\n")
	sb.WriteString("          \n")
	sb.WriteString("          if [[ -z \"$SOURCE_VERSION\" || \"$SOURCE_VERSION\" == \"null\" ]]; then\n")
	sb.WriteString("            log_warn \"No version found - using SHA as version\"\n")
	sb.WriteString("            SOURCE_VERSION=\"$SOURCE_SHA\"\n")
	sb.WriteString("          else\n")
	sb.WriteString("            log_success \"Source version: $SOURCE_VERSION\"\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          \n")
	sb.WriteString("          echo \"source_sha=$SOURCE_SHA\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("          echo \"source_version=$SOURCE_VERSION\" >> \"$GITHUB_OUTPUT\"\n")

	// Calculate semver tag
	sb.WriteString("      - name: Calculate Semver Tag\n")
	sb.WriteString("        id: semver\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          SOURCE_VERSION: ${{ steps.validate.outputs.source_version }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          # Strip RC suffix (e.g., v1.2.0-rc.3 -> v1.2.0)\n")
	fmt.Fprintf(sb, "          SEMVER_TAG=$(echo \"$SOURCE_VERSION\" | sed 's/%s//')\n", g.config.ResolveTagGrammar().PreReleaseStripSedBRE())
	sb.WriteString("          echo \"semver_tag=$SEMVER_TAG\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("          echo \"::notice::Semver tag: $SEMVER_TAG (from $SOURCE_VERSION)\"\n")

	// Check for breaking changes
	sb.WriteString("      - name: Setup CLI\n")
	fmt.Fprintf(sb, "        uses: stablekernel/cascade/.github/actions/setup-cli@%s\n", g.getCLIRef())
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())
	fmt.Fprintf(sb, "          version: %s\n", g.config.GetCLIVersion())
	sb.WriteString("      - name: Check Breaking Changes\n")
	sb.WriteString("        id: check\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          SOURCE_SHA: ${{ steps.validate.outputs.source_sha }}\n")
	// The gate reads the per-run workflow input by default. A repo that opts out
	// with allow_breaking_changes: true bakes the value on at generation time, so
	// a breaking release proceeds even when the operator leaves the input
	// unchecked. Mirrors how the tag grammar is baked from g.config.
	if g.config.AllowsBreakingChanges() {
		sb.WriteString("          ALLOW_BREAKING: \"true\"\n")
	} else {
		sb.WriteString("          ALLOW_BREAKING: ${{ github.event.inputs.allow_breaking_changes }}\n")
	}
	sb.WriteString("        run: |\n")
	sb.WriteString("          # Colorized logging helpers\n")
	sb.WriteString("          log_info() { echo -e \"\\033[36m[INFO]\\033[0m $1\"; }\n")
	sb.WriteString("          log_success() { echo -e \"\\033[32m[OK]\\033[0m $1\"; }\n")
	sb.WriteString("          log_warn() { echo -e \"\\033[33m[WARN]\\033[0m $1\"; }\n")
	sb.WriteString("          log_error() { echo -e \"\\033[31m[ERROR]\\033[0m $1\"; }\n")
	sb.WriteString("          log_decision() { echo -e \"\\033[35m[DECISION]\\033[0m $1\"; }\n")
	sb.WriteString("          \n")
	sb.WriteString("          log_info \"Checking for breaking changes\"\n")
	sb.WriteString("          \n")
	sb.WriteString("          # Get latest release SHA for comparison\n")
	fmt.Fprintf(sb, "          MANIFEST_FILE=\"%s\"\n", g.getManifestFilePath())
	fmt.Fprintf(sb, "          MANIFEST_KEY=\"%s\"\n", g.getManifestKey())
	sb.WriteString("          LATEST_SHA=$(yq eval \".$MANIFEST_KEY.latest_release.sha // \\\"\\\"\" \"$MANIFEST_FILE\" 2>/dev/null || echo \"\")\n")
	sb.WriteString("          if [[ -z \"$LATEST_SHA\" || \"$LATEST_SHA\" == \"null\" ]]; then\n")
	sb.WriteString("            log_info \"No previous release found - using initial commit\"\n")
	sb.WriteString("            LATEST_SHA=$(git rev-list --max-parents=0 HEAD | tail -n 1)\n")
	sb.WriteString("          else\n")
	sb.WriteString("            log_info \"Comparing against previous release: ${LATEST_SHA:0:7}\"\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          \n")
	sb.WriteString("          # Generate changelog and check for breaking changes\n")
	sb.WriteString("          log_info \"Generating changelog to detect breaking changes...\"\n")
	sb.WriteString("          RESULT=$(cascade generate-changelog --base-sha \"$LATEST_SHA\" --head-sha \"$SOURCE_SHA\" --repo \"${{ github.repository }}\")\n")
	sb.WriteString("          HAS_BREAKING=$(echo \"$RESULT\" | jq -r '.has_breaking // false')\n")
	sb.WriteString("          echo \"has_breaking=$HAS_BREAKING\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("          \n")
	sb.WriteString("          # Check if we can proceed\n")
	sb.WriteString("          if [[ \"$HAS_BREAKING\" == \"true\" ]]; then\n")
	sb.WriteString("            log_warn \"Breaking changes detected!\"\n")
	sb.WriteString("            if [[ \"$ALLOW_BREAKING\" == \"true\" ]]; then\n")
	sb.WriteString("              log_decision \"'allow_breaking_changes' is checked - proceeding\"\n")
	sb.WriteString("              echo \"can_proceed=true\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("            else\n")
	sb.WriteString("              log_error \"Breaking changes not allowed - check 'allow_breaking_changes' to proceed\"\n")
	sb.WriteString("              echo \"can_proceed=false\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("            fi\n")
	sb.WriteString("          else\n")
	sb.WriteString("            log_success \"No breaking changes detected\"\n")
	sb.WriteString("            echo \"can_proceed=true\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("          fi\n")

	// Fail if cannot proceed
	sb.WriteString("      - name: Fail if Cannot Proceed\n")
	// REDPROOF-FAULT (throwaway): inverted guard means the block step fires
	// only when can_proceed is true, so a breaking publish never gets blocked.
	sb.WriteString("        if: steps.check.outputs.can_proceed == 'true'\n")
	sb.WriteString("        run: exit 1\n\n")
}

func (g *ReleaseGenerator) writeReleaseJob(sb *strings.Builder) {
	sb.WriteString("  release:\n")
	sb.WriteString("    name: Release\n")
	sb.WriteString("    needs: preflight\n")
	sb.WriteString("    if: ${{ github.event.inputs.dry_run != 'true' }}\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    steps:\n")
	writeMintSteps(sb, g.config, "      ", seamRelease)
	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          fetch-depth: 0\n")

	// Setup CLI
	sb.WriteString("      - name: Setup CLI\n")
	fmt.Fprintf(sb, "        uses: stablekernel/cascade/.github/actions/setup-cli@%s\n", g.getCLIRef())
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())
	fmt.Fprintf(sb, "          version: %s\n", g.config.GetCLIVersion())

	// Generate changelog
	sb.WriteString("      - name: Generate Changelog\n")
	sb.WriteString("        id: changelog\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          SOURCE_SHA: ${{ needs.preflight.outputs.source_sha }}\n")
	sb.WriteString("        run: |\n")
	fmt.Fprintf(sb, "          MANIFEST_FILE=\"%s\"\n", g.getManifestFilePath())
	fmt.Fprintf(sb, "          MANIFEST_KEY=\"%s\"\n", g.getManifestKey())
	sb.WriteString("          # Get latest release SHA for changelog\n")
	sb.WriteString("          LATEST_SHA=$(yq eval \".$MANIFEST_KEY.latest_release.sha // \\\"\\\"\" \"$MANIFEST_FILE\" 2>/dev/null || echo \"\")\n")
	sb.WriteString("          if [[ -z \"$LATEST_SHA\" || \"$LATEST_SHA\" == \"null\" ]]; then\n")
	sb.WriteString("            LATEST_SHA=$(git rev-list --max-parents=0 HEAD | tail -n 1)\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          \n")
	sb.WriteString("          RESULT=$(cascade generate-changelog --base-sha \"$LATEST_SHA\" --head-sha \"$SOURCE_SHA\" --repo \"${{ github.repository }}\")\n")
	sb.WriteString("          echo \"changelog<<EOF\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("          echo \"$RESULT\" | jq -r '.changelog' >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("          echo \"EOF\" >> \"$GITHUB_OUTPUT\"\n")

	// Create draft release
	sb.WriteString("      - name: Create Draft Release\n")
	sb.WriteString("        if: ${{ github.event.inputs.release_action == 'create-draft' }}\n")
	fmt.Fprintf(sb, "        uses: %s\n", g.getActionPath())
	sb.WriteString("        with:\n")
	sb.WriteString("          repo: ${{ github.repository }}\n")
	sb.WriteString("          action: update\n")
	sb.WriteString("          environment: draft\n")
	sb.WriteString("          sha: ${{ needs.preflight.outputs.source_sha }}\n")
	sb.WriteString("          tag: ${{ needs.preflight.outputs.semver_tag }}\n")
	sb.WriteString("          changelog: ${{ steps.changelog.outputs.changelog }}\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())

	// Create prerelease
	sb.WriteString("      - name: Create Prerelease\n")
	sb.WriteString("        if: ${{ github.event.inputs.release_action == 'prerelease' }}\n")
	fmt.Fprintf(sb, "        uses: %s\n", g.getActionPath())
	sb.WriteString("        with:\n")
	sb.WriteString("          repo: ${{ github.repository }}\n")
	sb.WriteString("          action: prerelease\n")
	sb.WriteString("          environment: prerelease\n")
	sb.WriteString("          sha: ${{ needs.preflight.outputs.source_sha }}\n")
	sb.WriteString("          tag: ${{ needs.preflight.outputs.source_version }}\n")
	sb.WriteString("          new_tag: ${{ needs.preflight.outputs.semver_tag }}\n")
	sb.WriteString("          changelog: ${{ steps.changelog.outputs.changelog }}\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())

	// Publish release
	sb.WriteString("      - name: Publish Release\n")
	sb.WriteString("        if: ${{ github.event.inputs.release_action == 'release' }}\n")
	fmt.Fprintf(sb, "        uses: %s\n", g.getActionPath())
	sb.WriteString("        with:\n")
	sb.WriteString("          repo: ${{ github.repository }}\n")
	sb.WriteString("          action: publish\n")
	sb.WriteString("          environment: released\n")
	sb.WriteString("          sha: ${{ needs.preflight.outputs.source_sha }}\n")
	sb.WriteString("          tag: ${{ needs.preflight.outputs.semver_tag }}\n")
	sb.WriteString("          delete_tag: ${{ needs.preflight.outputs.source_version }}\n") // RC tag to find release
	sb.WriteString("          changelog: ${{ steps.changelog.outputs.changelog }}\n")
	fmt.Fprintf(sb, "          token: %s\n\n", g.getReleaseTokenRef())
}

func (g *ReleaseGenerator) writeFinalizeJob(sb *strings.Builder) {
	sb.WriteString("  finalize:\n")
	sb.WriteString("    name: Finalize\n")
	sb.WriteString("    needs: [preflight, release]\n")
	sb.WriteString("    if: always() && needs.preflight.result == 'success' && github.event.inputs.release_action == 'release'\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    steps:\n")
	writeMintSteps(sb, g.config, "      ", seamState)
	writeActionStep(sb, g.config, "      ", actionCheckout)

	// Update latest_release state
	sb.WriteString("      - name: Update Latest Release State\n")
	sb.WriteString("        if: ${{ github.event.inputs.dry_run != 'true' && needs.release.result == 'success' }}\n")
	sb.WriteString("        env:\n")
	fmt.Fprintf(sb, "          GH_TOKEN: %s\n", g.getStateTokenRef())
	sb.WriteString("          SEMVER_TAG: ${{ needs.preflight.outputs.semver_tag }}\n")
	sb.WriteString("          SOURCE_SHA: ${{ needs.preflight.outputs.source_sha }}\n")
	sb.WriteString("        run: |\n")
	fmt.Fprintf(sb, "          MANIFEST_FILE=\"%s\"\n", g.getManifestFilePath())
	fmt.Fprintf(sb, "          MANIFEST_KEY=\"%s\"\n", g.getManifestKey())
	writeGitConfigSteps(sb, g.config, "          ")

	// release.yaml runs on workflow_dispatch (--ref TAG); resolve a real
	// branch to push to, falling back to trunk_branch.
	sb.WriteString("          BRANCH=\"${GITHUB_REF##refs/heads/}\"\n")
	fmt.Fprintf(sb, "          BRANCH=\"${BRANCH:-%s}\"\n", g.config.TrunkBranch)
	sb.WriteString("          \n")

	// Function so the retry loop can re-apply yq edits after each
	// fetch+reset, avoiding rebase conflicts on the same yaml line.
	sb.WriteString("          apply_release_state_edits() {\n")
	sb.WriteString("            TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)\n")
	sb.WriteString("            yq eval -i \".$MANIFEST_KEY.latest_release.version = \\\"$SEMVER_TAG\\\"\" \"$MANIFEST_FILE\"\n")
	sb.WriteString("            yq eval -i \".$MANIFEST_KEY.latest_release.sha = \\\"$SOURCE_SHA\\\"\" \"$MANIFEST_FILE\"\n")
	sb.WriteString("            yq eval -i \".$MANIFEST_KEY.latest_release.released_on = \\\"$TIMESTAMP\\\"\" \"$MANIFEST_FILE\"\n")
	sb.WriteString("            yq eval -i \".$MANIFEST_KEY.latest_release.released_by = \\\"${{ github.triggering_actor }}\\\"\" \"$MANIFEST_FILE\"\n")
	sb.WriteString("          }\n")
	sb.WriteString("          \n")

	// Persist latest_release state to the trunk branch. On real GitHub this
	// writes through the Contents REST API so the commit is signed (Verified)
	// and can bypass branch protection with a capable token; in act/gitea it
	// pushes with the existing fetch/reset/reapply/commit/push retry loop.
	// release.yaml is workflow_dispatch-only so the race window is smaller than
	// orchestrate's, but a concurrent orchestrate state write can still collide,
	// so both paths retry on top of the latest tip.
	sb.WriteString("          echo \"Updating latest_release state\"\n")
	writeStateCommitPush(sb, "          ", stateWriteParams{
		applyFn:       "apply_release_state_edits",
		commitMessage: "chore: update latest_release state\n\nVersion: $SEMVER_TAG",
		noChangeLabel: "No latest_release state changes",
		successLabel:  "Pushed latest_release state",
		authorName:    g.config.GetGitUserName(),
		authorEmail:   g.config.GetGitUserEmail(),
	})

	// Summary
	sb.WriteString("      - name: Summary\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          RELEASE_ACTION: ${{ github.event.inputs.release_action }}\n")
	sb.WriteString("          SEMVER_TAG: ${{ needs.preflight.outputs.semver_tag }}\n")
	sb.WriteString("          SOURCE_SHA: ${{ needs.preflight.outputs.source_sha }}\n")
	sb.WriteString("          DRY_RUN: ${{ github.event.inputs.dry_run }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          {\n")
	sb.WriteString("            echo \"## Release Complete\"\n")
	sb.WriteString("            echo \"\"\n")
	sb.WriteString("            echo \"| Property | Value |\"\n")
	sb.WriteString("            echo \"|----------|-------|\"\n")
	sb.WriteString("            echo \"| Action | $RELEASE_ACTION |\"\n")
	sb.WriteString("            echo \"| Version | $SEMVER_TAG |\"\n")
	sb.WriteString("            echo \"| SHA | \\`$SOURCE_SHA\\` |\"\n")
	sb.WriteString("            if [[ \"$DRY_RUN\" == \"true\" ]]; then\n")
	sb.WriteString("              echo \"| Mode | **DRY RUN** |\"\n")
	sb.WriteString("            fi\n")
	sb.WriteString("          } >> \"$GITHUB_STEP_SUMMARY\"\n")
}

// writeConcurrency emits a top-level concurrency: block on the release workflow.
// Release runs write the shared GitHub Releases surface and shared tags, so two
// concurrent release dispatches race on those writes regardless of release_action
// (a create-draft and a release run still touch the same release and tags). The
// group key is therefore the bare workflow name, which serializes all release runs
// against each other. Queueing (cancel-in-progress: false) is safer than cancelling:
// a killed mid-flight release action may have already pushed a tag or published a
// release that the next run would then conflict with.
func (g *ReleaseGenerator) writeConcurrency(sb *strings.Builder) {
	sb.WriteString("concurrency:\n")
	if g.config.Concurrency != nil && g.config.Concurrency.Group != "" {
		fmt.Fprintf(sb, "  group: %s\n", g.config.Concurrency.Group)
	} else {
		sb.WriteString("  group: \"${{ github.workflow }}\"\n")
	}
	if g.config.Concurrency != nil {
		fmt.Fprintf(sb, "  cancel-in-progress: %t\n", g.config.Concurrency.CancelInProgress)
	} else {
		sb.WriteString("  cancel-in-progress: false\n")
	}
	sb.WriteString("\n")
}
