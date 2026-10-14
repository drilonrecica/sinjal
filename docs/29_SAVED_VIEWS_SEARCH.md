# Search, Filters and Saved Views

## Search

Global monitor search by:
- name
- tag
- public display label
- host/domain where safe for admin-only UI

## Built-in filters
- UP
- DOWN
- PENDING
- FLAPPING (monitors with `flapping_since` set)
- PAUSED
- monitor type
- tag
- TLS expiring
- slowest
- recently failing

## Saved views

Allow simple saved filter sets. Saved views are per user (viewers may save their own views).

Example:

```yaml
name: Infrastructure
filters:
  tags: [hetzner]
  states: [up, down, flapping]
sort: name
```

Do not make saved views a dashboard/widget builder.

## Built (M8-08)

`010_saved_views` creates the table (`spec/schema.sql`). `store.ViewFilters` is the one filter shape: the list column's query and a view's `filters_json` (`text`, `states`, `types`, `tags`, `tls_expiring`, `failing`), validated and normalized on save; sort is `name` (default) or `slowest`. Per-user CRUD is `store.CreateSavedView`, `UpdateSavedView`, `DeleteSavedView`, `ListSavedViews`, every one scoped by the session's user id; names are unique per user ignoring case, at most 40 characters, at most 30 views per user. "Slowest" is a sort rather than a filter, and "recently failing" means a failed check among the last 36 in the engine's memory. Routes: `docs/31_HTTP_ROUTES.md`.

## URL state

Where practical, filter/sort state should be represented in the URL for shareable admin navigation.
