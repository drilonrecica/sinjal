# Status Pages

## Multiple pages

One Sinjal instance may host multiple independent status pages.

Examples:
- public apps
- internal services
- business-specific service page
- personal infrastructure

## Visibility modes

1. Public
2. Authenticated
3. Password-protected
4. Unlisted

### Public
Anyone can access.

### Authenticated
Requires Sinjal viewer/admin login.

### Password-protected
Separate page password. No full user account required.

### Unlisted
Tokenized/unguessable URL and no navigation/index link.

Unlisted is not a substitute for strong authentication for highly sensitive information.

## Content

Public defaults:
- public display name
- state
- uptime
- recent incidents
- optional latency

Do not expose by default:
- internal URL
- private hostname
- private IP
- monitor configuration
- auth details
- diagnostic response snippets

## Groups

Static ordered groups.

Example:

```text
Web
  Website
  API

Infrastructure
  Database
  Storage
```

## Branding

Per page:
- title
- description
- logo
- accent
- theme
- show/hide "Powered by Sinjal"

Sinjal branding is slightly visible by default.

## Incident history

Default public history: 30 days.

Configurable per page.

Manual incident note may be shown when explicitly published: each manual note has a `published` flag (default off); only published notes of incidents of monitors on the page are shown.

## Custom hostnames

Map hostnames to pages:

```text
status.example.com -> page A
internal.example.com -> page B
```

Sinjal does not manage DNS or TLS certificates.

Reverse proxy/Coolify/Caddy handles TLS.

## JSON endpoint

Provide a small public representation appropriate to page visibility.

Example fields:
- page
- overall_status
- generated_at
- components/groups
- recent incidents

Do not expose internal IDs unnecessarily.

## RSS/Atom

Expose incident feed where page visibility permits.

For password/authenticated pages, do not create a public feed that bypasses access rules.
