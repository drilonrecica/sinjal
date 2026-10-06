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

Do not expose internal stack traces.

## Public status representation

Only public-safe fields.

## Rate limits

Heartbeat:
- lightweight abuse protection/token validation
- do not create a heavy global rate-limit subsystem

Login endpoints need stronger protection than normal authenticated API calls.

## OpenAPI

`spec/api.openapi.yaml` is a starting contract and should be kept aligned with implemented documented endpoints.
