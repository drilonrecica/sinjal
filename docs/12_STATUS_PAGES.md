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

URL shape (decision P0-15):
- `/s/{token}`, with `/s/{token}/api.json` and `/s/{token}/feed.xml`
- token: 128 bits from `crypto/rand`, base32 (lowercase, no padding), in the path so links survive copy/paste
- only the token hash is stored; the full URL is shown once on creation or regeneration (regenerating invalidates the old URL)
- responses send `X-Robots-Tag: noindex, nofollow` and `Referrer-Policy: no-referrer`
- the `/status/{slug}` path does not serve unlisted pages

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

## Uptime strip

- 90 daily bars per monitor, oldest left
- each bar uses **adjusted** uptime for that day (`10_INCIDENTS.md`), in the instance timezone
- days fully covered by excluded maintenance use a distinct maintenance style and label
- days before the monitor existed, or fully paused, are neutral "no data"
- every bar has a text tooltip/label (date, uptime, incident count); color is never the only signal
- the headline uptime figure for a page uses the same 90-day adjusted value

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
- logo (PNG or JPEG only; see Logo files)
- accent
- theme
- show/hide "Powered by Sinjal"

Sinjal branding is slightly visible by default.

### Logo files

- formats: PNG and JPEG only; SVG is rejected (scriptable content, XSS risk); WebP is not supported in v1 (would need an extra dependency)
- max 512 KB and 1024 × 1024 px
- validated by sniffed content type and `image.DecodeConfig`, not by file extension
- stored as `/data/uploads/<random>.<png|jpg>`
- served with the correct `Content-Type` and `X-Content-Type-Options: nosniff`

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

A request whose trusted Host matches a mapping serves **only** that page's public routes:
- `/` (the page)
- `/api.json`
- `/feed.xml`
- static assets and uploads used by the page
- `/healthz`

Everything else (admin UI, `/login`, `/setup`, `/events`, `/api/v1/*`, other pages) returns 404 on mapped hostnames. Admin access uses the instance's own base URL.

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
