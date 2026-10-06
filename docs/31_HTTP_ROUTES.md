# HTTP Route Map

This is a route ownership guide, not a requirement to expose every action as JSON.

## Public/system

```text
GET  /healthz
GET  /readyz
GET  /login
POST /login
POST /login/totp             # second sign-in step for accounts with TOTP
POST /login/passkey/begin    # passkey sign-in (JSON)
POST /login/passkey/finish
POST /logout
GET  /setup                  # only before initial admin exists; requires setup token
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

GET  /incidents
GET  /incidents/{id}
POST /incidents/{id}/note

GET  /status-pages
GET  /status-pages/new
POST /status-pages
GET  /status-pages/{id}/edit
POST /status-pages/{id}
POST /status-pages/{id}/delete

GET  /notifications
GET  /notifications/channels/new
POST /notifications/channels
POST /notifications/channels/{id}
POST /notifications/channels/{id}/test
POST /notifications/channels/{id}/delete

GET  /notifications/profiles/new
POST /notifications/profiles
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
GET  /fragments/overview
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
GET  /settings/data
GET  /settings/backup
GET  /settings/system
```

Use POST for browser state changes unless a deliberate method-override pattern is introduced.

## SSE

```text
GET /events
```

Authenticated admin/viewer stream.

Live fragments (GET/HEAD, admin and viewer, `no-store`; 404 for an unknown monitor). The checked address is only in the admin's version:

```text
GET /fragments/monitors/{id}/row
GET /fragments/monitors/{id}/header
```

Do not put secrets or full diagnostic snippets in SSE payloads.

## Status pages

Path-based:

```text
GET /status/{slug}
GET /status/{slug}/api.json
GET /status/{slug}/feed.xml
```

Unlisted:

```text
GET /s/{token}
GET /s/{token}/api.json
GET /s/{token}/feed.xml
```

Custom hostname:
- same page can render at `/`
- access is selected by configured hostname mapping
- mapped hostnames serve only that page's public routes, static assets and `/healthz`; all other routes return 404 (see `12_STATUS_PAGES.md`)

Do not create conflicting host mappings.

## API

See `docs/14_API.md` and `spec/api.openapi.yaml`.

## CSRF

Browser state-changing routes require CSRF validation.

Heartbeat token endpoints are machine endpoints and use token authentication rather than browser CSRF semantics.

`web.Routes` (`internal/web/routes.go`) is the single route table. Browser routes are mounted inside its session group (`LoadSession` + CSRF); machine endpoints, health checks and static assets are mounted outside it. Details in `13_AUTH_SECURITY.md` "CSRF". Inside the session group, pages sit behind `RequireAuth` and every state-changing app route behind `RequireAdmin` ("Authorization" in `13_AUTH_SECURITY.md`); a route test enforces this.

## Implemented (M2)

- `GET /monitors/new`, `POST /monitors`, `GET /monitors/{id}/edit`, `POST /monitors/{id}` (M2-17): admin only. A valid post saves, schedules (`Engine.Schedule`) and announces the monitor, then redirects (303) to the monitor's page; an invalid one is answered 422 with the form, every error at once and the typed values kept (secrets excepted). Unknown id: 404.
- `GET|HEAD /monitors/{id}` (M2-18): admins and viewers; `?tab=overview|history|incidents|configuration|diagnostics`, anything else is the overview. Unknown id: 404.
- `POST /monitors/{id}/pause|resume` (M2-18): admin only; 303 back to the monitor; idempotent.
- `POST /monitors/{id}/delete` (M2-18): admin only. Without `confirm=1` it answers 200 with the confirmation page and changes nothing; with it the monitor is deleted, `monitor.deleted` is sent and the answer is 303 to `/monitors`.
- `GET|HEAD /fragments/monitors` (M2-18): the list rows alone, for the list to refresh itself; admins and viewers, `no-store`.

## Implemented (M3)

- `GET|HEAD /maintenance` (M3-07): admins and viewers; windows in effect, upcoming and past, times in the instance time zone. `GET|HEAD /fragments/maintenance`: the same sections alone, for the list to refresh itself on `maintenance.updated`; `no-store`.
- `GET /maintenance/new`, `POST /maintenance`, `GET /maintenance/{id}/edit`, `POST /maintenance/{id}` (M3-07): admin only. A valid post stores the window, audits it (`maintenance.created|updated`), sends `maintenance.updated` and redirects (303) to `/maintenance`; an invalid one is answered 422 with every error at once and the typed values kept. Unknown id: 404.
- `POST /maintenance/{id}/delete` (M3-07): admin only; offered on the edit page. Without `confirm=1` it answers 200 with the confirmation page and changes nothing; with it the window is deleted, audited (`maintenance.deleted`), `maintenance.updated` is sent and the answer is 303 to `/maintenance`.
- `GET|HEAD /incidents`, `/incidents/{id}` (M3-11): admins and viewers; the list (active first, then the latest 100 ended) and one incident's timeline; times in the instance time zone; unknown id: 404. `GET|HEAD /fragments/incidents` (`?monitor={id}` for one monitor, as on its Incidents tab) and `/fragments/incidents/{id}`: the list and the timeline alone, for them to refresh themselves on `incident.*`; `no-store`.
- `POST /incidents/{id}/note` (M3-11): admin only. A note is trimmed, 1–1,000 characters; otherwise 422 with the page and the message. It becomes a `manual_note` event, is audited (`incident.noted`), sends `incident.updated` and answers 303 to the incident. Unknown id: 404.
- `GET|HEAD /` and `/fragments/overview` (M3 follow-up): admins and viewers; the problem strip, the monitors by state and the latest incidents; the fragment is the page body alone, refreshed on `incident.*`, `monitor.created|deleted`, and every minute; `no-store`. `GET /fragments/incidents?limit=n` (1–100) shortens the ended part of the list; anything else is the default 100.

## Implemented (M4)

- `GET|POST /api/v1/heartbeat/{token}`, `POST /api/v1/heartbeat` with `Authorization: Bearer <token>` (M4-05): machine endpoints outside the session group (no session, no CSRF). 204 when the beat is recorded, 404 for a missing, malformed or unknown token, 429 with `Retry-After: 60` over 60 requests a minute from one client address; `Cache-Control: no-store`; the body is not read. The route table guard lists them as public.
