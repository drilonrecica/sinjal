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

How each mode is enforced (M7-05) is in `13_AUTH_SECURITY.md` "Status page access": login with return for authenticated pages, a password form and a page-scoped signed cookie (7 days, invalidated by a password change, rate-limited attempts) for password pages, the token address with noindex headers for unlisted pages.

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

Implementation (M7-04): one read of a monitor's intervals covers the 90 local days (midnights from the calendar, so a daylight-saving day has 23 or 25 hours); each day is then computed in memory. A day is **up** at 100 % adjusted, **partial** with some downtime at or above 95 %, **down** below 95 %; **maintenance** when it has time but none left after excluded maintenance; **no data** with no time at all (before creation, fully paused). The bar's tooltip reads e.g. "Sat 28 Mar 2026: 99.30% uptime, 1 incident"; the strip is one `role="img"` with a text summary (days without incidents, with downtime, in maintenance, without data) for assistive technology. The page's headline figure is the adjusted 90-day uptime of all its services together (time-weighted).

## Rendering (M7-04)

`GET /status/{slug}` renders with separate public templates (`web/templates/public.templ`, `css/public.css`): no admin shell, no script, the page's own theme (never the visitor's) and accent.

- Overall status, worst first, paused services not counted: **Major outage** (every service down), **Partial outage** (some down), **Degraded performance** (pending or flapping), **Under maintenance** (a window covers a service now), **All systems operational**; "No services are monitored" when nothing is counted.
- A service shows its public name, its state in words with a glyph (Operational, Down, Checking, Unstable, Not monitored, Maintenance), the strip, its 90-day adjusted uptime and, when "show latency" is on, the last check's duration.
- Incidents of the page's services that are active or ended within `incident_days`, newest first (at most 50): the service's public name, started, how long it was (or has been) down, and its published notes. Never shown: the monitor's own name, its target (URL, host, IP, port), the incident summary or failure kind, check errors or response snippets, ids.
- "Powered by sinjal" is a small low-contrast footer, removed with the page setting.
- The figures are kept in memory for 10 s per page and shared by its visitors; saving the page (any admin change moves `updated_at`) makes the next visit read them again. Responses are `Cache-Control: no-store`.

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

The note form on an incident has a "Show on status pages" checkbox, off by default (M7-04). The choice is made when the note is written and cannot be changed later; the timeline marks published notes "On status pages".

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

Implementation (M7-06, `internal/web/hosts.go`): `HostRouter` runs before the route table. It takes the trusted Host (`proxy.Host`: `X-Forwarded-Host` only from a peer in `SINJAL_TRUSTED_PROXIES`, otherwise the request's own `Host`), lowercases it, drops the port, IPv6 brackets and a trailing dot, and looks it up in `status_page_hosts` (one primary-key read per request, about 14 µs). The base URL's own host is never looked up. A mapped hostname is answered by a separate small table that has only `GET|HEAD|POST /` (the page; POST is its password form, origin-checked), `GET|HEAD /healthz`, `/static/*` and `/uploads/{name}`; everything else is 404, `/readyz` included. `/api.json` and `/feed.xml` are served too (M7-07).

On a mapped hostname the page's own access rules apply, with two differences (owner decisions, M7-06):
- there are no sessions there (session cookies belong to the instance's own host), so an **authenticated** page answers 303 to `SINJAL_BASE_URL/status/{slug}`, where signing in returns to it; without a base URL it answers 403 "Sign in to Sinjal to see this page";
- an **unlisted** page is served at `/` with the same `noindex` and `no-referrer` headers: mapping a hostname to it is the admin's explicit choice. Its `/status/{slug}` path still does not serve it.

A password page's cookie has Path `/` on its hostname.

## JSON endpoint

Provide a small public representation appropriate to page visibility.

Example fields:
- page
- overall_status
- generated_at
- components/groups
- recent incidents

Do not expose internal IDs unnecessarily.

Implementation (M7-07, `internal/web/publicfeed.go`): `api.json` is built from the same cached view as the HTML page (so it shows exactly what the page shows) and has `page{title,description}`, `overall_status{state,text}`, `generated_at`, `uptime_90d`, `components[]{key,name,group,status,latency_ms,uptime_90d}` and `incidents[]{key,component,active,started_at,ended_at,notes[{at,message}]}`; `component` is the key of the incident's service. Keys are opaque: `HMAC-SHA256(page key, kind|page id|id)` as 16 lowercase base32 characters, stable for a page, different on every page, not reversible; no monitor, incident or page id is exposed. `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`.

## RSS/Atom

Expose incident feed where page visibility permits.

For password/authenticated pages, do not create a public feed that bypasses access rules.

Implementation (M7-07): `feed.xml` is Atom, one entry per incident listed on the page (title "<service>: ongoing|resolved", id from the opaque incident key, published notes in the content). Both formats go through the page's access code (`Public.serve`): public and unlisted pages are open (unlisted also sends `noindex` and `no-referrer` and exists only under its token), a password page answers **401 with a plain line** (no form, no redirect) until the request carries its page cookie, an authenticated page answers 401 without a session (also on a mapped hostname, where there are no sessions). The password cookie of a path-based page covers its `api.json` and `feed.xml` (same path prefix).
