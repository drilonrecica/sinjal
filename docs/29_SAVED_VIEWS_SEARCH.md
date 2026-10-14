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

**Where.** The list column's search box, chips and "More" panel (`03_INFORMATION_ARCHITECTURE.md` "Layout"). Code: `internal/web/views.go`, `internal/store/savedviews.go`, `ColumnPanel` (`web/templates/column_panel.templ`). Routes: `31_HTTP_ROUTES.md`.

**Search.** Case-insensitive substring match on the name, tags, public display labels and, for admins only, the target summary (`scheme://host`, `host:port`, …).

**Filters.**
- On screen: the chips All, Down, Up and Paused; in the More panel one type, one tag, "TLS expiring" and "Recently failing".
- In the URL and in saved views: any of `up`, `down`, `pending`, `flapping`, `paused`, several types and up to 10 tags.
- Values of one filter match any; filters combine with AND.
- "TLS expiring": the certificate expires within 30 days.
- "Recently failing": a failed check among the last 36 in the engine's memory.
- "Slowest" is a sort, not a filter: by the newest check's duration, monitors without a check last. The default sort is name.

**Saved views.**
- Table `saved_views` (`010_saved_views`, `spec/schema.sql`): name, `filters_json`, `sort`.
- `store.ViewFilters` is the one filter shape: the column's query and a view's `filters_json` (`text`, `states`, `types`, `tags`, `tls_expiring`, `failing`). It is validated and normalized on save.
- Per-user CRUD: `store.CreateSavedView`, `UpdateSavedView`, `DeleteSavedView`, `ListSavedViews`, each scoped by the session's user id.
- Names are unique per user ignoring case, at most 40 characters; at most 30 views per user.
- In the More panel: "Save view" stores the current filters under a name; Update replaces a view's filters with the current ones; Delete removes it. A view is a link to `/monitors` with its query.

## URL state

Where practical, filter/sort state should be represented in the URL for shareable admin navigation.

Built (M8-08): the list state is the query of the page address: `q`, `state`, `type`, `tag`, `tls=1`, `failing=1`, `sort=slowest`. A change in the column's form puts it in the address (`HX-Replace-Url`), and opening an address with it starts the column from it. Unknown or invalid values are dropped, never an error, so a stale link still shows the list.
