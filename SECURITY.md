# Security Policy

## Reporting

For a security-sensitive bug, use a private reporting channel configured by the repository owner. Do not include exploit details in a public GitHub issue.

## Scope

Security issues include:
- authentication bypass
- session fixation/hijacking
- secret disclosure
- unsafe backup behavior
- CSRF
- status-page access bypass
- privilege escalation from viewer to admin
- unsafe proxy-header trust
- unintended internal target disclosure
- remote code execution
- unsafe file handling

## Non-goals

Sinjal intentionally allows administrators to configure monitors for private/internal network addresses. This is a trusted-admin capability, not an SSRF vulnerability by itself.

## Supported versions

Before 1.0, security fixes target the latest release unless explicitly documented otherwise.
