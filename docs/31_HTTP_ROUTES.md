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
POST /maintenance/{id}
POST /maintenance/{id}/delete

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
