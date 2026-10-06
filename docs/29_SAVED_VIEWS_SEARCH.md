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

## URL state

Where practical, filter/sort state should be represented in the URL for shareable admin navigation.
