# Security Policy

## Supported versions

| Version | Supported |
| --- | --- |
| Latest release line | Yes, security fixes shipped promptly |
| Older releases | No |

The latest release line is the active one. Only the most recent release receives security patches. Upgrade to the latest release to stay covered.

The schema-version compatibility policy (which CLI versions read which manifest versions) is documented separately in [versioning and schema compatibility](https://stablekernel.github.io/cascade/reference/versioning/).

## Reporting a vulnerability

Please do **not** open a public GitHub issue for security vulnerabilities.

Report them privately via [GitHub Security Advisories](https://github.com/stablekernel/cascade/security/advisories/new) using the "Report a vulnerability" button on the repository's Security tab. This needs no email and keeps the report private until a fix is coordinated.

<!-- Maintainers: enable Private Vulnerability Reporting under Settings > Code
     security so the "Report a vulnerability" button is available. -->
Include a description of the issue, steps to reproduce, and any relevant version information.

**Response expectations**

- You will receive an acknowledgement within 3 business days.
- We aim to triage and confirm the issue within 7 days.
- A fix or mitigation will be released as soon as practicable, typically within 30 days for high-severity findings.

We follow [coordinated disclosure](https://en.wikipedia.org/wiki/Coordinated_vulnerability_disclosure): please allow us reasonable time to address the issue before making it public.

## Security model

Cascade is a build-time tool that generates GitHub Actions workflows you commit and review in your own repository. The generated workflows run under your own runners, branch protection, and environment gates, and cross-repo coordination uses a same-organization, shared-token model where a dispatch token you provision is the trust boundary. Deploying cascade safely is therefore a shared responsibility between cascade and your organization's GitHub and cloud configuration.

See the [security and hardening guide](https://stablekernel.github.io/cascade/security/) for the full model and a step-by-step hardening checklist.
