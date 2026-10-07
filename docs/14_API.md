# API

## Philosophy

Sinjal has a small documented API for automation.

It is not API-first, not GraphQL, and not intended as a large public developer platform.

The HTML/HTMX application may use server-rendered endpoints directly rather than forcing all UI actions through JSON.

## Versioning

Use `/api/v1/...`.

Breaking changes before 1.0 are allowed, but should be documented.

## Authentication

Admin API:
- session auth for browser use
- optional API token support may be added if required for owner automation, but do not add unless implemented deliberately

Heartbeat endpoint:
- token-authenticated

Public status API:
- follows status-page visibility/access rules

## Minimum endpoints

Suggested:

```text
GET  /api/v1/status
GET  /api/v1/monitors
GET  /api/v1/monitors/{id}
POST /api/v1/monitors/{id}/pause
POST /api/v1/monitors/{id}/resume

POST /api/v1/heartbeat/{token}

GET  /status/{slug}/api.json
GET  /status/{slug}/feed.xml
```

Creation/editing may remain normal application endpoints in v1 unless a concrete automation need exists.

## Admin API (M7-09)

`internal/web/api.go`. Session auth (the browser cookie). The routes sit in their own session group (`LoadSession`, no form CSRF), never redirect, and always answer JSON.

| Route | Who | Answer |
| --- | --- | --- |
| `GET /api/v1/status` | admin, viewer | `status` (`ok`, `degraded`, `down`), `generated_at`, `monitors`, `counts` by state (`up`, `down`, `flapping`, `pending`, `paused`), `problems` (down and flapping monitors) |
| `GET /api/v1/monitors` | admin, viewer | `{"monitors": [...]}` in name order |
| `GET /api/v1/monitors/{id}` | admin, viewer | `id`, `name`, `type`, `state`, `enabled`, `state_since`, `interval_seconds`, `last_check_at`, `last_success_at`, `last_failure_at`, `tls_not_after` |
| `POST /api/v1/monitors/{id}/pause`, `…/resume` | admin | 204; doing it twice is fine, and only a real change is audited (`monitor.paused`, `monitor.resumed`, the same events as the buttons) |

A monitor's target, configuration, failure text and snippets are not in the API (a target can carry secrets).

CSRF-equivalent protection for the two POSTs: the request must carry `X-Sinjal-Request: 1` (a custom header cannot be sent cross-site without a CORS preflight, which Sinjal never answers) and pass the same Origin check as the app's own state changes (`Sec-Fetch-Site`, else `Origin`; neither header means a script, which passes). Order of checks: sign-in (401), then Origin and header (403), then role (403). Times are UTC RFC 3339. Not served on mapped status page hostnames.

## Error format

JSON errors should be consistent:

```json
{
  "error": {
    "code": "monitor_not_found",
    "message": "Monitor not found"
  }
}
```

Codes: `unauthenticated` (401), `forbidden` (403, viewer), `cross_origin` and `missing_header` (403), `monitor_not_found` (404), `internal` (500).

Do not expose internal stack traces.

## Public status representation

Only public-safe fields.

## Rate limits

Heartbeat:
- lightweight abuse protection/token validation
- do not create a heavy global rate-limit subsystem
- implemented (M4-05): `GET|POST /api/v1/heartbeat/{token}` and `POST /api/v1/heartbeat` with a bearer token; 204, 404 for an unknown token, 429 over 60 requests a minute per client address (the bounded `ratelimit.Limiter`, 10,000 addresses). Details in `06_MONITORING_ENGINE.md` "Heartbeat"

Login endpoints need stronger protection than normal authenticated API calls.

## OpenAPI

`spec/api.openapi.yaml` is a starting contract and should be kept aligned with implemented documented endpoints.
