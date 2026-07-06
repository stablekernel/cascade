package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// actionFolderRe mirrors the config package's action_folder charset check.
// This is a defense-in-depth guard: config validation already rejects an
// unsafe action_folder, but the generator refuses to join an unsafe value
// into a filesystem path rather than trusting the caller validated it.
var actionFolderRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// RenderLocalActions returns the composite action file the manifest would
// generate, paired with its rendered content, without writing anything to disk.
// The path is baseDir/.github/actions/<folder>/action.yaml where <folder> is
// cfg.GetActionFolder() (default: "manage-release"). ownRepo emits cascade's
// own-repo variant of the action (the extra tag_only input its own-repo
// orchestrate finalize step sets); every downstream manifest gets the plain
// action, byte-identical to before own-repo mode existed.
func RenderLocalActions(baseDir string, cfg *config.TrunkConfig, ownRepo bool) (PlannedFile, error) {
	actionFolder := cfg.GetActionFolder()
	if strings.Contains(actionFolder, "..") || strings.Contains(actionFolder, "/") || !actionFolderRe.MatchString(actionFolder) {
		return PlannedFile{}, fmt.Errorf("action_folder %q is not a safe plain folder name", actionFolder)
	}
	actionPath := filepath.Join(baseDir, ".github", "actions", actionFolder, "action.yaml")
	return PlannedFile{Path: actionPath, Content: generateManageReleaseAction(ownRepo)}, nil
}

// GenerateLocalActions creates the local action files in the user's repo.
// Uses cfg.GetActionFolder() for the folder name (default: "manage-release").
// See RenderLocalActions for the ownRepo semantics.
func GenerateLocalActions(baseDir string, cfg *config.TrunkConfig, ownRepo bool) error {
	action, err := RenderLocalActions(baseDir, cfg, ownRepo)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(action.Path), 0755); err != nil {
		return fmt.Errorf("creating actions directory: %w", err)
	}

	if err := os.WriteFile(action.Path, []byte(action.Content), 0644); err != nil {
		return fmt.Errorf("writing action file: %w", err)
	}

	return nil
}

// generateManageReleaseAction returns the content for the local manage-release
// action. ownRepo adds the tag_only input (and its env/CLI-arg forwarding),
// cascade's own-repo release plumbing: cutting the tag only, without
// pre-creating a draft release, so the self-publishing release workflow
// (GoReleaser) is the sole creator of the release object. It is additive and
// omitted entirely when ownRepo is false, so downstream generation is
// byte-identical to the action every manifest has always produced.
func generateManageReleaseAction(ownRepo bool) string {
	var sb strings.Builder

	sb.WriteString(GeneratedFileMarker)
	sb.WriteString(`
# Regenerate with: cascade generate-workflow

name: 'Manage Release'
description: 'Create, update, lock, prerelease, publish, or delete GitHub releases'

inputs:
  repo:
    description: 'Repository in owner/repo format'
    required: true
  action:
    description: 'Action to perform: create, update, lock, prerelease, publish, delete'
    required: true
  environment:
    description: 'Target environment'
    required: true
  sha:
    description: 'Release commit SHA'
    required: true
  tag:
    description: 'Tag name'
    required: true
  changelog:
    description: 'Release notes markdown'
    required: false
    default: ''
  token:
    description: 'GitHub token with repo permissions'
    required: true
  previous_tag:
    description: 'Previous tag for changelog comparison'
    required: false
    default: ''
  new_tag:
    description: 'New semver tag for prerelease action'
    required: false
    default: ''
  delete_tag:
    description: 'Tag to delete after publish'
    required: false
    default: ''
  create_tag:
    description: 'Create git tag on create action'
    required: false
    default: 'false'
`)
	if ownRepo {
		sb.WriteString(`  tag_only:
    description: 'Create the git tag only and skip creating a draft release'
    required: false
    default: 'false'
`)
	}
	sb.WriteString(`
outputs:
  release_id:
    description: 'GitHub release ID'
    value: ${{ steps.manage.outputs.release_id }}
  release_url:
    description: 'API URL to the release'
    value: ${{ steps.manage.outputs.release_url }}
  html_url:
    description: 'Browser URL to the release'
    value: ${{ steps.manage.outputs.html_url }}

runs:
  using: 'composite'
  steps:
    - name: Manage Release
      id: manage
      shell: bash
      env:
        INPUT_REPO: ${{ inputs.repo }}
        INPUT_ACTION: ${{ inputs.action }}
        INPUT_ENVIRONMENT: ${{ inputs.environment }}
        INPUT_SHA: ${{ inputs.sha }}
        INPUT_TAG: ${{ inputs.tag }}
        INPUT_CHANGELOG: ${{ inputs.changelog }}
        INPUT_PREVIOUS_TAG: ${{ inputs.previous_tag }}
        INPUT_NEW_TAG: ${{ inputs.new_tag }}
        INPUT_DELETE_TAG: ${{ inputs.delete_tag }}
        INPUT_CREATE_TAG: ${{ inputs.create_tag }}
`)
	if ownRepo {
		sb.WriteString("        INPUT_TAG_ONLY: ${{ inputs.tag_only }}\n")
	}
	sb.WriteString(`        GITHUB_TOKEN: ${{ inputs.token }}
      run: |
        # Write changelog to temp file to handle multiline content
        CHANGELOG_FILE=$(mktemp)
        printf '%s' "$INPUT_CHANGELOG" > "$CHANGELOG_FILE"

        # Build command arguments
        CMD_ARGS=(
          --repo "$INPUT_REPO"
          --action "$INPUT_ACTION"
          --environment "$INPUT_ENVIRONMENT"
          --sha "$INPUT_SHA"
          --tag "$INPUT_TAG"
        )
        [[ -n "$INPUT_PREVIOUS_TAG" ]] && CMD_ARGS+=(--previous-tag "$INPUT_PREVIOUS_TAG")
        [[ -n "$INPUT_NEW_TAG" ]] && CMD_ARGS+=(--new-tag "$INPUT_NEW_TAG")
        [[ -n "$INPUT_DELETE_TAG" ]] && CMD_ARGS+=(--delete-tag "$INPUT_DELETE_TAG")
        [[ "$INPUT_CREATE_TAG" == "true" ]] && CMD_ARGS+=(--create-tag)
`)
	if ownRepo {
		sb.WriteString(`        [[ "$INPUT_TAG_ONLY" == "true" ]] && CMD_ARGS+=(--tag-only)
`)
	}
	sb.WriteString(`
        # Run CLI
        OUTPUT=$(cascade manage-release "${CMD_ARGS[@]}" --changelog-file "$CHANGELOG_FILE")
        rm -f "$CHANGELOG_FILE"

        # Parse and write outputs
        echo "release_id=$(echo "$OUTPUT" | sed -n '1p')" >> "$GITHUB_OUTPUT"
        echo "release_url=$(echo "$OUTPUT" | sed -n '2p')" >> "$GITHUB_OUTPUT"
        echo "html_url=$(echo "$OUTPUT" | sed -n '3p')" >> "$GITHUB_OUTPUT"
`)

	return sb.String()
}
