# Frontend Component Boundaries

Sinjal uses templ + HTMX + CSS + minimal vanilla JS.

## Shared layout

- app shell
- sidebar
- mobile nav/drawer
- top context bar
- problem strip
- toast region
- modal/dialog primitive only where appropriate
- command palette

## Monitor components

- monitor row
- status badge
- latency sparkline
- uptime metric
- tag list
- dependency indicator
- state duration
- history chart
- availability timeline
- incident timeline
- diagnostics summary

## Form primitives

- text input
- select
- checkbox/switch
- duration input
- secret input
- tag picker
- collapsible advanced section
- validation summary

Do not create a massive generic component framework.

## Status page components

Separate public presentation components from admin components where privacy/branding differs.

- page header
- group
- service row
- incident summary
- uptime strip
- powered-by footer

## JavaScript responsibilities

Allowed:
- uPlot chart initialization/update
- command palette keyboard handling
- local pre-hydration theme choice
- small accessibility helpers
- clipboard copy
- optional dialog focus management

Avoid using JS for:
- normal navigation
- basic form submission
- monitor list rendering
- global app state
- client-side routing

## HTMX responsibilities

Use for:
- fragment refresh
- CRUD form submission
- pause/resume actions
- live problem strip/row updates
- pagination/filter updates where useful

### Monitor list

`MonitorList` (`web/templates/monitors.templ`, styles in `css/monitors.css`) renders `MonitorRow`s inside `Live()`. The empty state opens no event stream.

### Live fragments

`MonitorRow` and `MonitorHeader` (fed by `MonitorView`, built in `internal/web/monitors.go`) are layout-free fragments served at `/fragments/monitors/{id}/row|header`. Mark an element `data-live` with `data-monitor-id` and an `hx-get` of its fragment, put it inside `Live()`, and it refreshes when the monitor's `monitor.updated` event arrives (see `32_SSE_EVENTS.md`, browser side). A page using `Live` adds `templates.LiveScripts` to its assets. `StatusBadge` is the one status icon plus text; its glyph is `aria-hidden` and differs in shape per state.

### Monitor form

`MonitorFormPage` (`web/templates/monitor_form.templ`, `css/monitor_form.css` on top of the form primitives in `css/auth.css`) is fed by `MonitorForm`, which keeps numbers as typed so a rejected value comes back unchanged. The translation to `store.HTTPMonitor` (seconds → ms, KiB → bytes, `Name: value` lines → `headers_json`, assertion rows → `json_assertions_json`) lives in `internal/web/monitor_form.go`. Primitives used: text input and select through `field` (label, hint, error with `aria-describedby` / `aria-invalid`), `checkbox`, `numberField`, secret inputs with a "keep" note, the collapsible Advanced section and a validation summary (`role="alert"`, links in page order).

## Styling

Use custom CSS/token system.

A utility CSS build step may be used only if it does not force a stock visual language or production runtime. The preferred direction is authored CSS with tokens/components.

## Static assets

Files under `web/static/` are embedded in the binary (`web.Static`) and served by `internal/assets` at `/static/<name>.<content-hash>.<ext>`:

- templates get URLs from `assets.URL("css/base.css")`; an unknown name panics, so every template is rendered in tests
- hashed URLs are cached `public, max-age=31536000, immutable`; the unhashed name is not served (404)
- CSS, JS, SVG, JSON and text are gzip-compressed once at startup and served to clients that accept gzip (distinct ETag per representation, `Vary: Accept-Encoding`)
- vendored third-party files start with a comment recording upstream URL, exact version and licence (`40_DEPENDENCIES.md`)
