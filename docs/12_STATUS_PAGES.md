# Status Pages

Public and private pages that show the state of selected monitors. Access control is in `13_AUTH_SECURITY.md` "Status page access"; field validation is in `38_CONFIG_VALIDATION.md` "Status page".

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

Enforcement (M7-05) is described in `13_AUTH_SECURITY.md` "Status page access":
- authenticated: sign-in, then back to the page
- password: a password form and a page-scoped signed cookie (7 days, invalidated by a password change, rate-limited attempts)
- unlisted: the token address only, with the noindex headers above

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

Implementation (M7-04): `internal/web/strip.go`, shared with a monitor's Overview.
- One read of a monitor's intervals covers the 90 local days. Midnights come from the calendar, so a daylight-saving day has 23 or 25 hours. Each day is then computed in memory.
- Day classes:
  - **up**: 100 % adjusted
  - **partial**: some downtime, at or above 95 %
  - **down**: below 95 %
  - **maintenance**: the day has time, but none is left after excluded maintenance
  - **no data**: no time at all (before creation, fully paused)
- A bar's tooltip reads, for example, "Sat 28 Mar 2026: 99.30% uptime, 1 incident".
- The strip is one `role="img"` (`templates.DayBars`) whose label summarises it for assistive technology: days without incidents, with downtime, in maintenance, without data.
- The page's headline figure is the adjusted 90-day uptime of all its services together (time-weighted).

## Rendering (M7-04)

`GET /status/{slug}` renders with separate public templates (`web/templates/public.templ`, `css/public.css`): no admin shell, no script, the page's own theme (never the visitor's) and accent.

Overall status, worst first; paused services are not counted:
1. **Major outage**: every service down
2. **Partial outage**: some down
3. **Degraded performance**: pending or flapping
4. **Under maintenance**: a window covers a service now
5. **All systems operational**

"No services are monitored" when nothing is counted.

Services: each shows its public name, its state in words with a glyph (Operational, Down, Checking, Unstable, Not monitored, Maintenance), the 90-day strip, its 90-day adjusted uptime and, when "show latency" is on, the last check's duration.

Incidents:
- Listed: incidents of the page's services that are active or ended within `incident_days`, newest first, at most 50.
- Shown: the service's public name, when it started, how long it was (or has been) down, and its published notes.
- Never shown: the monitor's own name, its target (URL, host, IP, port), the incident summary or failure kind, check errors or response snippets, ids.

Refresh (M7-08):
- The page reloads itself every 60 s with `<meta http-equiv="refresh" content="60">`. There is no script, no HTMX, and never the admin SSE stream (`/events` needs a session and is not served on mapped hostnames).
- The password form and message pages do not refresh.
- A script-free reload keeps the public CSP free of any public script and needs no event plumbing. The 10 s view cache below keeps a minute of visitors cheap.

Cache: the figures are kept in memory for 10 s per page and shared by its visitors. Saving the page (any admin change moves `updated_at`) makes the next visit read them again. Responses are `Cache-Control: no-store`.

Footer: "Powered by sinjal" is a small low-contrast footer, removed with the page setting.

Built (M8-17, Clear):
- The page opens with its hero: the overall status as one sentence. It is the overall state's words (the strings above, unchanged for `api.json` and the feed) and, during an outage, the public names of the services that are down ("Partial outage: Checkout is down."; three names, then "and n more").
- The Ambient light of the app's heroes sits behind it: red for an outage, amber when degraded, the accent otherwise; a faint halo in Daylight, none in Terminal.
- The newest published note of an ongoing incident is shown right under the sentence (service, time, text), so visitors read the explanation first. Every note also stays with its incident below.
- Services draw the shared 90-day bars (`DayBars`, as on a monitor's Overview).
- A page whose theme is unknown or empty renders in Daylight, the status page default (`templates.PublicDefaultTheme`), never Stark. New pages also start in Daylight (`statuspage.DefaultTheme`).

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

Implementation (M7-03): `internal/statuspage/logo.go`, `internal/web/uploads.go`.
- The file name is 128 random bits in hex (`<32 hex>.png` or `.jpg`), created exclusively with mode 0600 in `<data dir>/uploads`.
- `GET /uploads/{name}` needs no session, since a public page shows its logo to everyone. Only names of the stored form are looked up, so no other file can be reached.
- Files are served with the type their extension names, never sniffed, and cached for good (`immutable`): a name is never reused.

## Incident history

Default public history: 30 days.

Configurable per page.

Manual incident note may be shown when explicitly published: each manual note has a `published` flag (default off); only published notes of incidents of monitors on the page are shown.

Built (M7-04): the note form on an incident has a "Show on status pages" checkbox, off by default. The choice is made when the note is written and cannot be changed later. The timeline marks published notes "On status pages". `incident_days` ranges from 1 to 365.

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

Implementation (M7-06, `internal/web/hosts.go`):
- `HostRouter` runs before the route table. It takes the trusted Host (`proxy.Host`: `X-Forwarded-Host` only from a peer in `SINJAL_TRUSTED_PROXIES`, otherwise the request's own `Host`).
- It lowercases the host, drops the port, IPv6 brackets and a trailing dot, and looks it up in `status_page_hosts`: one primary-key read per request, about 14 µs (`18_PERFORMANCE.md`). The base URL's own host is never looked up.
- A mapped hostname is answered by a separate small table with only:
  - `GET|HEAD|POST /`: the page; POST is its password form, origin-checked
  - `GET|HEAD /api.json` and `/feed.xml` (M7-07)
  - `GET|HEAD /healthz`
  - `/static/*` and `/uploads/{name}`
- Everything else is 404, `/readyz` included.

On a mapped hostname the page's own access rules apply, with two differences (owner decisions, M7-06):
- There are no sessions there (session cookies belong to the instance's own host). So an **authenticated** page answers 303 to `SINJAL_BASE_URL/status/{slug}`, where signing in returns to it. Without a base URL it answers 403 "Sign in to Sinjal to see this page".
- An **unlisted** page is served at `/` with the same `noindex` and `no-referrer` headers: mapping a hostname to it is the admin's explicit choice. Its `/status/{slug}` path still does not serve it.

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

Implementation (M7-07, `internal/web/publicfeed.go`): `api.json` is built from the same cached view as the HTML page, so it shows exactly what the page shows.

Fields:
- `page{title,description}`
- `overall_status{state,text}`
- `generated_at`
- `uptime_90d`
- `components[]{key,name,group,status,latency_ms,uptime_90d}`
- `incidents[]{key,component,active,started_at,ended_at,notes[{at,message}]}`; `component` is the key of the incident's service

Keys are opaque: an HMAC-SHA256 with the page key over the kind, the page id and the id, as 16 lowercase base32 characters. They are stable for a page, different on every page and not reversible; no monitor, incident or page id is exposed.

Headers: `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`.

## RSS/Atom

Expose incident feed where page visibility permits.

For password/authenticated pages, do not create a public feed that bypasses access rules.

Implementation (M7-07): `feed.xml` is Atom, one entry per incident listed on the page. Each entry has the title "<service>: ongoing|resolved", an id from the opaque incident key, and the published notes as content.

Both formats go through the page's access code (`Public.serve`):
- public and unlisted pages are open; unlisted also sends `noindex` and `no-referrer` and exists only under its token
- a password page answers **401 with a plain line** (no form, no redirect) until the request carries its page cookie
- an authenticated page answers 401 without a session, also on a mapped hostname, where there are no sessions

The password cookie of a path-based page covers its `api.json` and `feed.xml` (same path prefix).
