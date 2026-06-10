# Security Policy

## Supported versions

| Version | Supported |
| --- | --- |
| 0.x (latest) | Yes, security fixes backported promptly |
| Older 0.x tags | No |

The `0.x` line is the active release line. Only the most recent tag receives
security patches. Upgrade to the latest release to stay covered.

The schema-version compatibility policy (which CLI versions read which manifest
versions) is documented separately in
[docs/versioning.md](docs/versioning.md).

## Reporting a vulnerability

Please do **not** open a public GitHub issue for security vulnerabilities.

Report them privately via [GitHub Security Advisories](https://github.com/stablekernel/cascade/security/advisories/new).
Include a description of the issue, steps to reproduce, and any relevant
version information.

**Response expectations**

- You will receive an acknowledgement within 3 business days.
- We aim to triage and confirm the issue within 7 days.
- A fix or mitigation will be released as soon as practicable, typically within
  30 days for high-severity findings.

We follow [coordinated disclosure](https://en.wikipedia.org/wiki/Coordinated_vulnerability_disclosure):
please allow us reasonable time to address the issue before making it public.
