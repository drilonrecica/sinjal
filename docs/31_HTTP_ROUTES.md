# HTTP Route Map

This is a route ownership guide. It does not require every action to be exposed as JSON.

`web.Routes` (`internal/web/routes.go`) is the single route table. How it is grouped and guarded is under [CSRF](#csrf).

## Public/system

```text
GET  /healthz
GET  /readyz
GET  /static/*               # embedded assets under content-hashed names, cached immutably
GET  /favicon.ico            # 301 to the hashed icon (/static/img/favicon.<hash>.svg); also on mapped status-page hosts
GET  /login
POST /login
POST /login/totp             # second sign-in step for accounts with TOTP
POST /login/passkey/begin    # passkey sign-in (JSON)
POST /login/passkey/finish
POST /logout
GET  /setup                  # only before the initial admin exists; requires the setup token
POST /setup                  # see 13_AUTH_SECURITY.md "Initial setup"
```

## App

```text
GET  /
GET  /monitors
GET  /monitors/new
POST /monitors
GET  /monitors/{id}
GET  /monitors/{id}/edit
POST /monitors/{id}
POST /monitors/{id}/pause
POST /monitors/{id}/resume
POST /monitors/{id}/delete
POST /monitors/{id}/heartbeat/token   # new push URL, shown once; recent re-authentication

POST /views                    # save the column's filters as a view (docs/29); admin or viewer, own views only
POST /views/{id}               # rename or replace its filters
POST /views/{id}/delete

GET  /incidents
GET  /incidents/{id}
POST /incidents/{id}/note      # publish=1 also shows it on status pages

GET  /status-pages
GET  /status-pages/new
POST /status-pages
GET  /status-pages/{id}/edit
POST /status-pages/{id}
POST /status-pages/{id}/token   # new unlisted address, shown once; recent re-authentication
POST /status-pages/{id}/logo     # multipart: PNG/JPEG logo, replaces the old one (docs/12 "Logo files")
POST /status-pages/{id}/logo/delete
POST /status-pages/{id}/delete   # without confirm=1 it asks first

GET  /notifications
GET  /notifications/channels/new
POST /notifications/channels
GET  /notifications/channels/{id}/edit
POST /notifications/channels/{id}
POST /notifications/channels/{id}/test   # 303 to …/edit?test=sent|failed
POST /notifications/channels/{id}/delete

GET  /notifications/profiles/new
POST /notifications/profiles
GET  /notifications/profiles/{id}/edit
POST /notifications/profiles/{id}
POST /notifications/profiles/{id}/simulate
POST /notifications/profiles/{id}/delete

GET  /maintenance
GET  /maintenance/new
POST /maintenance
GET  /maintenance/{id}/edit
POST /maintenance/{id}
POST /maintenance/{id}/delete
GET  /fragments/maintenance
GET  /fragments/notifications      # the channel list
GET  /fragments/overview
GET  /fragments/monitors           # the list column's rows; the column's filters as query (docs/29)
GET  /fragments/column-filters     # the column's More panel: type, tag, sort, flags, saved views
GET  /fragments/palette            # ?q= command palette results (docs/03)
GET  /fragments/activity           # Live activity column, from 2200 px
GET  /fragments/incidents          # ?monitor={id} narrows it, ?limit=n (1-100) shortens it
GET  /fragments/incidents/{id}     # the timeline of one incident

GET  /reauth                 # confirm password (+ TOTP code) before a sensitive action
POST /reauth
POST /reauth/passkey/begin   # or confirm with a passkey (JSON)
POST /reauth/passkey/finish

GET  /settings/general
GET  /settings/appearance
GET  /settings/authentication
GET  /settings/authentication/totp           # admin, recent re-authentication
POST /settings/authentication/totp           # enable
POST /settings/authentication/totp/disable
POST /settings/authentication/passkeys/begin         # add a passkey (JSON); admin, recent re-authentication
POST /settings/authentication/passkeys/finish
POST /settings/authentication/passkeys/{id}/delete
POST /settings/authentication/viewers                # create a viewer; admin, recent re-authentication
POST /settings/authentication/viewers/{id}/disable   # also deletes the viewer's sessions
POST /settings/authentication/viewers/{id}/enable
GET  /account/password       # own password change; admin or viewer, recent re-authentication
POST /account/password
POST /account/sessions/sign-out-others   # own other sessions; admin or viewer, recent re-authentication
GET  /account/appearance     # own theme, density, Ambient light; admin or viewer
POST /account/appearance
GET  /settings/data
GET  /settings/backup
GET  /settings/system
```

Use POST for browser state changes unless a deliberate method-override pattern is introduced.

### Built behaviour

Unless stated otherwise: list, detail and fragment routes answer `GET` and `HEAD` (create and edit forms answer `GET` only), fragments are `no-store`, and an unknown id is 404.

Monitors:

- `GET /monitors/new`, `POST /monitors`, `GET /monitors/{id}/edit`, `POST /monitors/{id}` (M2-17): admin only.
  - A valid post saves, schedules (`Engine.Schedule`) and announces the monitor, then redirects (303) to the monitor's page.
  - An invalid post is answered 422 with the form, every error at once and the typed values kept (secrets excepted).
- `GET /monitors/new?type=tcp|icmp|dns|heartbeat` (M4-06): the create form for that type; anything else is HTTP. `POST /monitors` takes the type from the form field `type`. `POST /monitors/{id}` ignores it: a monitor's type never changes. A created heartbeat monitor is answered 200 with the one-time push URL page instead of the 303.
- `GET /monitors/{id}` (M2-18): admins and viewers. `?tab=overview|history|incidents|configuration|diagnostics`; anything else is the overview.
- `POST /monitors/{id}/pause|resume` (M2-18): admin only; 303 back to the monitor; idempotent.
- `POST /monitors/{id}/delete` (M2-18): admin only. Without `confirm=1` it answers 200 with the confirmation page and changes nothing. With it, the monitor is deleted, `monitor.deleted` is sent and the answer is 303 to `/monitors`.
- `POST /monitors/{id}/heartbeat/token` (M4-06): admin group plus `RequireRecentAuth`.
  - Issues a new token for a heartbeat monitor (404 for another type or an unknown id) and audits `monitor.heartbeat_token_regenerated`.
  - Answers 200 with the one-time push URL page (`no-store`).
  - A stale session is sent to `/reauth`, with the Referer (the Configuration tab) as `next`.

List column and saved views:

- `GET /fragments/monitors` (M2-18, M8-08): the list rows alone, so the list can refresh itself; admins and viewers.
  - The query is the list state (docs/29): `q`, repeatable `state` (up, down, pending, flapping, paused), `type` and `tag`, `tls=1`, `failing=1`, `sort=slowest`. Invalid values are dropped.
  - The answer's `hx-get` keeps the state. When the column's own form asked (`HX-Trigger: column-filter`), the answer carries `HX-Replace-Url` (the current page's path with the state); this is the URL state.
- `GET /fragments/column-filters` (M8-08): the column's More panel (type, tag, sort, TLS expiring, recently failing, the caller's saved views); same query; admins and viewers.
- `POST /views`, `POST /views/{id}`, `POST /views/{id}/delete` (M8-08): viewers too, own views only.
  - They create a view (`view_name` plus the list state fields), replace a view's filters and sort, and delete a view.
  - Another user's id is answered as "no longer exists".
  - Each answers with the refreshed More panel (200, validation messages inside it).

Maintenance:

- `GET /maintenance` (M3-07): admins and viewers; windows in effect, upcoming and past, times in the instance time zone. `GET /fragments/maintenance` is the same sections alone, refreshed on `maintenance.updated`.
- `GET /maintenance/new`, `POST /maintenance`, `GET /maintenance/{id}/edit`, `POST /maintenance/{id}` (M3-07): admin only.
  - A valid post stores the window, audits it (`maintenance.created|updated`), sends `maintenance.updated` and redirects (303) to `/maintenance`.
  - An invalid post is answered 422 with every error at once and the typed values kept.
- `POST /maintenance/{id}/delete` (M3-07): admin only; offered on the edit page. Without `confirm=1` it answers 200 with the confirmation page and changes nothing. With it, the window is deleted, audited (`maintenance.deleted`), `maintenance.updated` is sent and the answer is 303 to `/maintenance`.

Incidents and overview:

- `GET /incidents`, `GET /incidents/{id}` (M3-11): admins and viewers. The list shows active incidents first, then the latest 100 ended ones; the detail is one incident's timeline. Times are in the instance time zone.
- `GET /fragments/incidents` and `/fragments/incidents/{id}`: the list and the timeline alone, refreshed on `incident.*`. `?monitor={id}` narrows the list to one monitor (its Incidents tab). `?limit=n` (1–100) shortens the ended part; anything else is the default 100.
- `POST /incidents/{id}/note` (M3-11): admin only. The note is trimmed and must be 1–1,000 characters; otherwise 422 with the page and the message. It becomes a `manual_note` event, is audited (`incident.noted`), sends `incident.updated` and answers 303 to the incident.
- `GET /` and `/fragments/overview` (M3 follow-up): admins and viewers; the problem strip, the monitors by state and the latest incidents. The fragment is the page body alone, refreshed on `incident.*`, `monitor.created|deleted`, and every minute.

Command palette:

- `GET /fragments/palette?q=` (M8-06): the command palette's monitor group; admins and viewers.
  - Matches, ignoring case, a monitor's name, tags, public display labels and, for admins only, the target summary the column shows (scheme and host, never a path or query).
  - Returns the first 8 by name; an empty `q` lists none.
  - Admins get Pause or Resume per monitor: buttons that submit the palette's form to `POST /monitors/{id}/pause|resume` (`formaction`, CSRF field), boosted like any shell form.
  - The palette's theme options post to `POST /account/appearance` with the page's current density and Ambient light.

## SSE

```text
GET /events
```

Authenticated admin/viewer stream (`32_SSE_EVENTS.md`).

Live fragments (admins and viewers; 404 for an unknown monitor). The checked address appears only in the admin's version:

```text
GET /fragments/monitors/{id}/row
GET /fragments/monitors/{id}/header
GET /fragments/monitors/{id}/beats    # ?until= a list row's beat tooltips (JSON, newest first, from memory)
```

Do not put secrets or full diagnostic snippets in SSE payloads.

## Status pages

Logos are public and need no session: a public page shows its logo to everyone, and the name is 128 random bits.

```text
GET /uploads/{name}   # name = 32 hex + .png|.jpg, anything else is 404; Content-Type from the extension, X-Content-Type-Options: nosniff, immutable cache
```

Path-based (M7-04). `GET|HEAD /status/{slug}` renders public pages. It sits in the session group outside `RequireAuth`; see `12_STATUS_PAGES.md` "Rendering".

```text
GET /status/{slug}
GET /status/{slug}/api.json
GET /status/{slug}/feed.xml
```

`api.json` and `feed.xml` (M7-07) are `GET|HEAD` only. They enforce the same access as the page and answer a refusal with 401 and a plain line (no form, no redirect); see `12_STATUS_PAGES.md` "JSON endpoint".

Unlisted (M7-05): `GET|HEAD /s/{token}`. `POST /status/{slug}` is the page password form. Access rules are in `13_AUTH_SECURITY.md` "Status page access".

```text
GET /s/{token}
GET /s/{token}/api.json
GET /s/{token}/feed.xml
```

Custom hostname (M7-06, `internal/web/hosts.go`; details in `12_STATUS_PAGES.md` "Custom hostnames"):

- the same page can render at `/`
- access is selected by the configured hostname mapping
- a mapped hostname serves only that page's public routes (`/`, `/api.json`, `/feed.xml`), static assets, uploads and `/healthz`; every other route returns 404 (see `12_STATUS_PAGES.md`)

Do not create conflicting host mappings.

## API

See `docs/14_API.md` and `spec/api.openapi.yaml`.

Built routes:

```text
GET  /api/v1/status
GET  /api/v1/monitors
GET  /api/v1/monitors/{id}
POST /api/v1/monitors/{id}/pause
POST /api/v1/monitors/{id}/resume
GET  /api/v1/heartbeat/{token}
POST /api/v1/heartbeat/{token}
POST /api/v1/heartbeat               # Authorization: Bearer <token>
```

Admin API (M7-09, `docs/14_API.md` "Admin API"):

- JSON only, in its own session group (`LoadSession`, `RegisterAPI`); reads also answer `HEAD`.
- 401 JSON when not signed in. Reads for viewers; POSTs for admins.
- POSTs need the `X-Sinjal-Request: 1` header and pass the Origin check instead of the form token.
- Not served on mapped hostnames.

Heartbeat (M4-05):

- Machine endpoints outside the session group: no session, no CSRF. The route table guard lists them as public.
- 204 when the beat is recorded; 404 for a missing, malformed or unknown token.
- 429 with `Retry-After: 60` over 60 requests a minute from one client address.
- `Cache-Control: no-store`; the body is not read.

## CSRF

Browser state-changing routes require CSRF validation.

Heartbeat token endpoints are machine endpoints. They use token authentication rather than browser CSRF semantics.

How `web.Routes` applies this:

- Browser routes are mounted inside its session group (`LoadSession` + CSRF). Machine endpoints, health checks and static assets are mounted outside it. Details are in `13_AUTH_SECURITY.md` "CSRF".
- Inside the session group, pages sit behind `RequireAuth`, and every state-changing app route sits behind `RequireAdmin` (`13_AUTH_SECURITY.md` "Authorization").
- A route test (`TestRouteTableGuards`) enforces this.
