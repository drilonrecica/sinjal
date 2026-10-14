# Frontend Component Boundaries

Sinjal uses templ + HTMX + CSS + minimal vanilla JS.

## Shared layout

- app shell
- monitor list column
- Live activity column
- phone menu
- top context bar
- problem strip
- toast region
- modal/dialog primitive only where appropriate
- command palette

Built (M8-05 … M8-10):
- **App shell:** `Shell` (`web/templates/shell.templ`): the top bar (`topnav`, `PaletteButton`, the account menu), the list column, the pane `#main` (`data-view` is `list` or `detail`, for phones) and the Live activity column, all inside `Live` (the one event stream). Every shell page gets the same assets, `ShellStyles` and `ShellScripts` (`web/templates/page.go`).
- **List column:** `ColumnList` / `ColumnRow` (`web/templates/monitors.templ`). The search box, the chips and the More panel are one htmx form in the shell that asks `/fragments/monitors` for the filtered list. The More panel is `ColumnPanel` (`web/templates/column_panel.templ`), loaded on first open from `/fragments/column-filters`: type, tag, sort, the TLS-expiring and failing flags, and the user's saved views (save the current filters, Update, Delete; posting to `/views`). `js/shell.js` only sets the chip's hidden field.
- **Live activity:** `ActivityList` (`web/templates/activity.templ`), filled from `/fragments/activity` by `js/shell.js`.
- **Phone menu:** the top bar's sections in a `<details>`, open without JavaScript and closed by `js/shell.js` on phones.
- **Problem strip:** see "Overview components".
- **Toast region:** `ToastRegion` (`shell.templ`), filled by `js/toast.js` (see "Styling").
- **Command palette:** `Palette` / `PaletteButton` / `PaletteMonitors` (`web/templates/palette.templ`), a `<dialog>` in the shell. A combobox that owns one grouped listbox: the monitors `/fragments/palette` finds (Open; Pause/Resume for admins), Go to, Create (admins) and Theme. Run by `js/palette.js` (Ctrl/Cmd+K, ↑/↓, Enter, Escape).

## Monitor components

- monitor row
- status badge
- beat bar
- 90-day day bars
- uptime metric
- tag list
- dependency indicator
- state duration
- history chart
- availability timeline
- incident timeline
- diagnostics summary

## Overview components

All in `OverviewBody` (`web/templates/overview.templ`), which refreshes as a whole:
- Home hero (`.home-hero`): the status sentence (`statusSentence`, `internal/web/overview.go`) with the Ambient light, keyed on `data-state`
- problem strip: a bordered list, glyph and text per problem (never colour alone), only when there are problems; `data-severity` (down or warning) sets its border
- maintenance notes: `MaintenanceNotes` (see "Maintenance components")
- state counts: the figures row of the monitor pages
- recent incidents: `IncidentRows`, the incident list without its own refresh
- recent events: `eventList` of `RecentEvent` lines from `store.RecentIncidentEvents`, labels from the incident timeline's `eventLabels`
- layout: `.home-grid` with `.home-main` and `.home-side`, two columns from 1600 px

## History components

In `web/templates/charts.templ`:
- range selector (`RangeSelector`): preset links and a custom range form, both plain GET to `/monitors/{id}?tab=history`; on the History tab only
- history panel (`HistoryPanel`): summary, figures, latency chart and availability timeline (`AvailabilityTimeline`); on the History tab with the selector, and on the Overview tab for the last 24 hours without it

## Incident components

In `web/templates/incidents.templ`:
- incident list (`IncidentList`, rows `IncidentRows`): one compact row per incident (status glyph and text, monitor, summary, start, duration, markers); the whole page body on `/incidents` and on the monitor's Incidents tab; `data-live-incidents`, refetched from its own address on any `incident.*` event
- incident timeline (`IncidentTimeline`): an ordered list of times, labels and messages; notes are shown as escaped, wrapped text; the admin's note form sits outside the refreshed part so a draft survives a refresh

## Maintenance components

In `web/templates/maintenance.templ`:
- maintenance list (`MaintenanceList`, `MaintenanceSections`): sections In effect now / Upcoming / Past, one compact row per window (name, schedule, when, scope, effect)
- maintenance notes (`MaintenanceNotes`): on Home and a monitor page, the windows in effect and those beginning within 24 hours, each a wrench, the name linking to Maintenance, "in effect until …" / "scheduled from …" and, on Home, the scope; nothing when there are none
- maintenance form (`MaintenanceFormPage`): when (name, start, duration, repeats with weekdays), effect (hold back notifications, exclude from adjusted uptime), monitors (all, or chosen monitors and tags); delete on the edit page

## Notification components

In `web/templates/notifications.templ`:
- channel list (`ChannelSection`): one compact row per channel (name, type, health in words, last success and failure); the live part of the page
- profile list: one row per profile (name, monitors using it, routes, quiet hours, reminder)
- routing matrix (`ProfileFormPage`): a table, channels as rows and severities as columns, one checkbox per cell with a visually hidden label ("Critical to Mail"); it scrolls inside its own box at phone width
- send result: `auth-notice` (`role="status"`) when sent, `auth-alert` (`role="alert"`) with the sender's error when not; the simulation page lists one row per message

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

Built (M7-04, M8-17) in `web/templates/public.templ`:
- `PublicLayout`: its own document with the page's theme, an optional hashed accent style, and no script
- `PublicStatusPage` with the hero (`.public-hero`, with the Ambient light), `publicHeader`, `publicGroup`, `publicRow` (service row with `DayBars` as the uptime strip) and `publicIncidents`
- view types in `public.go` carry public names and figures only
- styles in `css/public.css`

## JavaScript responsibilities

Allowed:
- uPlot chart initialization/update
- command palette keyboard handling
- keyboard shortcuts
- local pre-hydration theme choice
- small accessibility helpers
- clipboard copy
- beat bar tooltip, keyboard movement and slide-in
- optional dialog focus management

Avoid using JS for:
- normal navigation
- basic form submission
- monitor list rendering
- global app state
- client-side routing

Built: every shell page loads the same scripts (`ShellScripts`), deferred, in this order:
- `htmx.min.js`, `htmx-ext-sse.js`: htmx and its SSE extension
- `live.js`: routes stream events to `data-live*` elements (see "Live fragments")
- `beats.js`: beat bar tooltip, keyboard movement and slide-in; fetches a list row's tips on the first hover
- `shell.js`: marks the current section and row, list column keys, chips, Live activity loading, phone menu
- `palette.js`: the command palette
- `shortcuts.js`: `/`, `g d`, `g m`, `g i`, `n`; off while typing, with a dialog open or with Ctrl/Cmd/Alt held
- `uplot.min.js`, `chart.js`: reads `data-series`, `data-overlays` and `data-tz` of `[data-chart]`, draws overlays in uPlot hooks, redraws on resize and theme change, and sets up charts swapped into the pane (`htmx.onLoad`)
- `copy.js`: clipboard copy (`button[data-copy]`, hidden until the script finds the Clipboard API)
- `passkey.js`: WebAuthn ceremonies
- `toast.js`: toasts, failed-request feedback, `aria-busy`, stream lost/resumed notes

The local pre-hydration is the inline `templates.PrehydrateScript` (`05_THEMES.md` "Theme persistence"). Public status pages run no script.

## HTMX responsibilities

Use for:
- fragment refresh
- CRUD form submission
- pause/resume actions
- live problem strip/row updates
- pagination/filter updates where useful

### Pane navigation

Links and forms in the shell are boosted (`hx-boost`). The server aims a boosted answer at `#main` (`HX-Retarget`/`HX-Reselect`, `boostToPane` in `internal/web/boost.go`); fragments set no `hx-target` for this. Details: `03_INFORMATION_ARCHITECTURE.md` "Layout".

### Monitor list

Since M8-05 there is no full-width list page. The monitors are the shell's list column: `ColumnList`, loaded from `/fragments/monitors`, a `data-live-list` that refetches itself when a monitor is created or deleted, maintenance changes or a channel's health moves. `/monitors` is the phones' list view (Home in the pane on larger screens).

### Live fragments

`ColumnRow` and `MonitorHeader` (fed by `MonitorView`, built in `internal/web/monitors.go`) are layout-free fragments served at `/fragments/monitors/{id}/row|header`.

- Mark an element `data-live` with `data-monitor-id` and an `hx-get` of its fragment, and it refreshes when its monitor's `monitor.updated` event arrives from the shell's stream (`32_SSE_EVENTS.md`, browser side).
- `data-live-list` elements refetch whole on `monitor.created`, `monitor.deleted`, `maintenance.updated` and `notification.channel_updated`; `data-live-incidents` elements on any `incident.*` event.
- When a stream (re)opens, everything it feeds refreshes once.
- `StatusBadge` (`monitor_row.templ`) is the one status icon plus text. Its glyph is `aria-hidden` and differs in shape per state.

### Beat bar and day bars

`BeatBar` (`web/templates/bars.templ`, styles in `components.css`) draws a `BeatBarView`, built by `beatBar` in `internal/web/beats.go`.

- **Source:** the engine's rings (`Engine.Recent()`, `internal/recent`): 120 checks per monitor, pushed by the result processor after each commit and seeded from the database at start.
- **Markup:** one `<i data-b="up|down">` per check, newest first, padded with empty slots (`--beat-none`). The bar is a reversed flex row that clips its oldest beats, so the width sets how many show. It is `role="img"` with the counts and the latest check in words.
- **List row:** 36 beats without tips, plus a `data-tips` URL (`/fragments/monitors/{id}/beats?until=<newest>`): a JSON array of tips, newest first, lined up with the beats on the page.
- **Detail header:** 120 beats with `data-tip`s and `tabindex="0"`.
- **`beats.js`:** one shared `role="tooltip"`; ←/→/Home/End move through a focused bar; focus survives a live swap; a bar slides by one beat when its `data-newest` grew.

`DayBars` (same file) is the 90-day strip of the detail Overview and of the public status page (since M8-17). It is computed by `internal/web/strip.go`, shared by both.

### Monitor detail

`MonitorDetail` (`web/templates/monitor_detail.templ`, `css/monitor_detail.css`) is fed by `MonitorDetailView`, built in `internal/web/monitor_detail.go`, which also removes what viewers may not see.

- Tabs are links with `aria-current="page"`, marked by weight and a bar as well as colour.
- `MonitorDeleteConfirm` is the delete step.

### Monitor form

`MonitorFormPage` (`web/templates/monitor_form.templ`, `css/monitor_form.css` on top of the form primitives in `css/auth.css`) is fed by `MonitorForm`, which keeps numbers as typed so a rejected value comes back unchanged.

- The translation to `store.MonitorInput` lives in `internal/web/monitor_form.go`: seconds → ms, KiB → bytes, `Name: value` lines → `headers_json`, assertion rows → `json_assertions_json`, expected DNS values one per line.
- Each type renders only its own section. The type is a row of links on create (`.type-switch`) and text on edit.
- `HeartbeatTokenPage` (`web/templates/heartbeat_token.templ`) is the one-time push URL page; its copy buttons come from `js/copy.js`.
- Primitives used: text input and select through `field` (label, hint, error with `aria-describedby` / `aria-invalid`), `checkbox`, `numberField`, secret inputs with a "keep" note (`keepNote`), the collapsible Advanced section, and a validation summary (`role="alert"`, links in page order).

## Styling

Use custom CSS/token system.

A utility CSS build step may be used only if it does not force a stock visual language or production runtime. The preferred direction is authored CSS with tokens/components.

Built (M8-01, M8-10):
- `css/tokens.css`: the three themes (`05_THEMES.md`)
- `css/base.css`: fonts, type and spacing scale, focus, icons, motion durations, Terminal's mono body, reduced motion
- `css/components.css`, loaded on every page, admin and public:
  - the logo
  - `.button` with `-primary`, `-danger`, `-quiet`
  - the status badge `.status[data-state]`; on a monitor header, the filled status block
  - the Ambient light behind `.monitor-header`, `.home-hero` and `.public-hero` (`[data-ambient="on"] … ::before`)
  - form fields (`.auth-field`, `.auth-hint`, `.auth-error`)
  - notices (`.auth-notice`, `role="status"`) and alerts (`.auth-alert`, `role="alert"`)
  - link tabs (`.tabs`, `.tab-link[aria-current]`) and `.tag`
  - beat bars and day bars
  - the toast look (`.toast-region`, `.toast`)
  - the Terminal and Daylight personalities
- Feature stylesheets lay these out and do not restyle them.
- Toasts: `ToastRegion` holds a polite `role="status"` list and an assertive `role="alert"` list. `js/toast.js` fills them from `sinjal:toast` events, `HX-Trigger: toast` headers and failed htmx requests. Error toasts persist until dismissed; others leave on their own. A failed list column, More panel or Live activity load gets an in-place Retry. The event stream is announced only when lost for 8 s and when back. A request's target gets `aria-busy`. Behaviour: `22_ACCESSIBILITY.md` "Toasts".
- Templates: `Icon(name)`, `Mark()` and `Wordmark()` in `web/templates/icons.templ`; `StatusBadge` in `monitor_row.templ`.

## Static assets

Files under `web/static/` are embedded in the binary (`web.Static`) and served by `internal/assets` at `/static/<name>.<content-hash>.<ext>`:

- templates get URLs from `assets.URL("css/base.css")`; an unknown name panics, so every template is rendered in tests
- hashed URLs are cached `public, max-age=31536000, immutable`; the unhashed name is not served (404)
- CSS, JS, SVG, JSON and text are gzip-compressed once at startup and served to clients that accept gzip (distinct ETag per representation, `Vary: Accept-Encoding`)
- vendored third-party files start with a comment recording upstream URL, exact version and licence (`40_DEPENDENCIES.md`); fonts, which cannot, are recorded in `40` and ship their OFL text beside them
- a relative `url(...)` in a stylesheet is rewritten to the referenced asset's hashed name at startup (fonts in `base.css`), so a stylesheet's own hash changes with what it references; a reference to a missing asset fails startup
