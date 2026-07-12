package generate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// nativeDeploymentsEnabled reports whether the manifest opted in to GitHub
// Deployments API reporting in the finalize job.
func nativeDeploymentsEnabled(cfg *config.TrunkConfig) bool {
	return cfg.Deployments.IsEnabled()
}

// deploymentAutoInactive returns the auto_inactive value sent to the Deployments
// API. KeepPriorActive maps to auto_inactive:false so GitHub leaves prior
// deployments for the same environment Active; the default relies on GitHub's
// native auto-inactivation (auto_inactive:true).
func deploymentAutoInactive(cfg *config.TrunkConfig) bool {
	return !cfg.Deployments.KeepsPriorActive()
}

// writeNativeDeploymentSteps emits the GitHub Deployments API lifecycle for the
// finalize job: create a Deployment for the runtime-selected environment, mark
// it in_progress, then report a terminal success/failure status. Every step is
// guarded to real GitHub via appTokenServerGuard so the workflow stays runnable
// on act/gitea, where the Deployments API is absent.
//
// envExpr is the shell-safe expression that resolves to the target environment
// name at run time (it differs between the orchestrate and promote seams).
// resultExpr is the shell expression that evaluates to "success" or "failure"
// for the deploy outcome. indent is the per-step indent (matching the
// surrounding generated YAML).
func writeNativeDeploymentSteps(sb *strings.Builder, cfg *config.TrunkConfig, envExpr, resultExpr, indent string) {
	if !nativeDeploymentsEnabled(cfg) {
		return
	}

	body := indent + "  "

	// Create the Deployment. The environment name is resolved at run time, so the
	// URL lookup is a shell case over the configured environment_config entries.
	sb.WriteString(indent + "- name: Create deployment\n")
	sb.WriteString(body + "id: cascade-deployment\n")
	sb.WriteString(body + "if: " + appTokenServerGuard + "\n")
	sb.WriteString(body + "env:\n")
	sb.WriteString(body + "  GH_TOKEN: ${{ github.token }}\n")
	sb.WriteString(body + "run: |\n")
	fmt.Fprintf(sb, "%s  ENV_NAME=\"%s\"\n", body, envExpr)
	fmt.Fprintf(sb, "%s  deployment_id=$(gh api repos/${{ github.repository }}/deployments \\\n", body)
	sb.WriteString(body + "    --method POST \\\n")
	sb.WriteString(body + "    --field ref=${{ github.sha }} \\\n")
	sb.WriteString(body + "    --field environment=\"$ENV_NAME\" \\\n")
	sb.WriteString(body + "    --field auto_merge=false \\\n")
	sb.WriteString(body + "    --raw-field required_contexts='[]' \\\n")
	fmt.Fprintf(sb, "%s    --field auto_inactive=%t \\\n", body, deploymentAutoInactive(cfg))
	sb.WriteString(body + "    --jq '.id')\n")
	sb.WriteString(body + "  echo \"deployment_id=${deployment_id}\" >> \"$GITHUB_OUTPUT\"\n")

	// Mark the deployment in_progress.
	sb.WriteString(indent + "- name: Set deployment in_progress\n")
	sb.WriteString(body + "if: " + appTokenServerGuard + "\n")
	sb.WriteString(body + "env:\n")
	sb.WriteString(body + "  GH_TOKEN: ${{ github.token }}\n")
	sb.WriteString(body + "run: |\n")
	fmt.Fprintf(sb, "%s  gh api repos/${{ github.repository }}/deployments/${{ steps.cascade-deployment.outputs.deployment_id }}/statuses \\\n", body)
	sb.WriteString(body + "    --method POST \\\n")
	sb.WriteString(body + "    --field state=in_progress\n")

	// Report the terminal status. always() lets it run even when a deploy failed,
	// so the Deployment never sticks at in_progress. The URL is selected by the
	// runtime environment name from the configured environment_config entries.
	sb.WriteString(indent + "- name: Set deployment status\n")
	sb.WriteString(body + "if: ${{ " + stripTokenExprWrapper(appTokenServerGuard) + " && always() }}\n")
	sb.WriteString(body + "env:\n")
	sb.WriteString(body + "  GH_TOKEN: ${{ github.token }}\n")
	sb.WriteString(body + "run: |\n")
	fmt.Fprintf(sb, "%s  ENV_NAME=\"%s\"\n", body, envExpr)
	fmt.Fprintf(sb, "%s  if [ \"%s\" = \"success\" ]; then state=\"success\"; else state=\"failure\"; fi\n", body, resultExpr)
	writeEnvironmentURLCase(sb, cfg, body)
	fmt.Fprintf(sb, "%s  gh api repos/${{ github.repository }}/deployments/${{ steps.cascade-deployment.outputs.deployment_id }}/statuses \\\n", body)
	sb.WriteString(body + "    --method POST \\\n")
	sb.WriteString(body + "    --field state=\"$state\" \\\n")
	sb.WriteString(body + "    --field environment_url=\"$environment_url\"\n")
}

// writeEnvironmentURLCase emits a shell case that sets $environment_url from the
// per-environment environment_config.<env>.environment_url entries, keyed on the
// runtime $ENV_NAME. Environments without a configured URL fall through to the
// empty default. Entries are emitted in sorted order for byte-stable output.
func writeEnvironmentURLCase(sb *strings.Builder, cfg *config.TrunkConfig, body string) {
	urls := make(map[string]string)
	for _, entry := range cfg.Environments {
		if entry.EnvironmentURL != "" {
			urls[entry.Name] = entry.EnvironmentURL
		}
	}
	sb.WriteString(body + "  environment_url=\"\"\n")
	if len(urls) == 0 {
		return
	}
	names := make([]string, 0, len(urls))
	for name := range urls {
		names = append(names, name)
	}
	sort.Strings(names)
	sb.WriteString(body + "  case \"$ENV_NAME\" in\n")
	for _, name := range names {
		fmt.Fprintf(sb, "%s    %s) environment_url=\"%s\" ;;\n", body, name, urls[name])
	}
	sb.WriteString(body + "  esac\n")
}
