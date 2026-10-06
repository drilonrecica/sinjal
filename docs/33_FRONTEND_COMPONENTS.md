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

## Styling

Use custom CSS/token system.

A utility CSS build step may be used only if it does not force a stock visual language or production runtime. The preferred direction is authored CSS with tokens/components.
