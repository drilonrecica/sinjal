# Design System

## Direction

Sinjal should look like a premium modern developer tool with precision-infrastructure influence.

The direction is **"Clear"** (owner decision 2026-10-14, after eight concept rounds): Uptime Kuma's clarity, beaten on craft.

- One job per region, few elements, generous air.
- The quality is in type, contrast, alignment, accurate numbers, keyboard paths and motion, never in more widgets.
- When a screen feels empty, it gets more of the same meaningful content (more history, a second column with its own job), not decoration.

Layout: `03_INFORMATION_ARCHITECTURE.md`. Themes: `05_THEMES.md`.

It must not look like:
- a stock admin template
- a generic AI-generated Tailwind dashboard
- a consumer wellness app
- a neon hacker toy
- an enterprise BI suite

## Typography

Fonts (decision P0-06):
- primary UI: Inter
- technical/mono: JetBrains Mono
- self-hosted variable woff2, subset to Latin + Latin Extended-A (covers e.g. ç ë č ć š đ ž)
- weight ranges: Inter 400–700, JetBrains Mono 400–600
- `font-display: swap`; served from embedded static assets with content-hashed URLs and immutable caching
- preload only the Inter file; the mono file loads on first use
- status pages use the same files
- no CDN, no Google Fonts requests
- system fallbacks: `system-ui, -apple-system, "Segoe UI", Roboto, sans-serif` and `ui-monospace, "SF Mono", "Cascadia Mono", Menlo, Consolas, monospace`

Use monospace selectively for:
- hostnames
- IP addresses
- ports
- timings
- IDs
- code/config snippets

Do not use monospace for all body text (the Terminal theme is the one deliberate exception).

Scale (Inter, 14 px base, `css/base.css`):
- sizes: `--text-xs` 12, `--text-sm` 13, `--text-base` 14, `--text-lg` 17, `--text-xl` 22 px
- weights 400 / 500 / 600; headings with slightly negative tracking
- numbers are tabular (`.num`), with units set smaller and lighter than the value ("142 ms", "99.98 %")
- uptime is never shown above 100 %

## Density

Two user-selectable modes:
- Comfortable — default
- Compact

Drive density through CSS tokens, not separate components.

Built (M8-04): density scales spacing through `--space-density-factor` (comfortable 1, compact 0.8, `css/tokens.css`). Each user's density is saved with their theme and Ambient light (Settings → Appearance, `/account/appearance`; `05_THEMES.md` "Theme persistence").

## Status semantics

Always combine:
- color
- icon/shape
- text

Core semantics:
- UP: positive/green family
- WARNING (e.g. TLS expiring; an indicator, not a state): amber family
- DOWN/CRITICAL: red family
- PAUSED/UNKNOWN: neutral gray
- PENDING: neutral/amber transitional
- FLAPPING: warning semantic with explicit label

Never rely on color alone.

## Spacing

- one 4 px scale: `--space-1` … `--space-6` = 4, 8, 12, 16, 24, 32 px, times the density factor
- controls are 36 px high with a mouse and 44 px on touch screens (`--control-h`)

## Geometry

Theme-dependent but restrained:
- typical radii: 2–10 px (Terminal 2–4 px)
- no giant pill-shaped panels
- pills acceptable for small tags/badges only

## Surfaces

Use:
- subtle borders
- restrained elevation
- clear hierarchy
- theme-specific depth
- one decorative gradient only: the **Ambient light** (below); chart area fills are the other gradient

### Ambient light

A single soft radial light behind the status hero: a monitor's header, the Home status sentence and (since M8-17) the public status page's hero.

- Its colour follows the state: `--ambient-up` (the accent family) when healthy, `--ambient-down` when down, `--ambient-flapping` when flapping.
- It is a user setting, on by default: `<html data-ambient="on|off">`, rendered by the server, no script.
- Never on text, never a glow on edges, no glass or blur.
- With `prefers-reduced-motion` its colour changes without a transition.
- Terminal draws none (`05_THEMES.md`).

## Motion

Motion marks change and nothing else; nothing loops:
- a new beat slides into its beat bar
- a status badge or block settles when its state changes
- rows that need attention move to the top of the list
- the Ambient light changes colour with the state
- toast entrance/exit, panel expand/collapse, chart live update, command palette

With `prefers-reduced-motion` every state is shown at once, without transitions.

Built (M8-11, M8-16):
- two duration tokens in `base.css`: `--motion-fast` (160 ms) and `--motion-settle` (420 ms)
- status badges and blocks change colour over `--motion-fast`
- the Ambient light's colour is a registered custom property (`@property --ambient`), so it transitions over `--motion-settle`
- a new beat slides in over `--motion-settle` (`js/beats.js`)
- toasts slide in and fade out, and the command palette drops in, over `--motion-fast`
- one `prefers-reduced-motion` rule in `base.css` reduces every animation and transition to nothing; `toast.js` then removes a toast at once

Not built yet: rows that need attention moving to the top of the list, panel expand/collapse and chart live update (charts draw once).

## Components

Shared in `web/static/css/components.css` (full list in `33_FRONTEND_COMPONENTS.md`):
- **status badge**: icon + word, tinted by the state; filled **status block** on a monitor's hero
- **beat bar**: one rounded mark per recent check, coloured by result, the newest slightly brighter; the count follows the available width (more history on wider screens, never bigger beats); hover and focus show time, latency and result
- **buttons**: one `.button` with primary, danger and quiet variants
- **fields**, **notices** (`role="status"`) and **alerts** (`role="alert"`), **link tabs**, **tags**, **toasts**

## Icons

Use simple inline SVG icons. Prefer a consistent set such as Lucide, without shipping a runtime icon library.

Built (M8-01): a Lucide subset as `Icon(name)` (`web/templates/icons.templ`, shapes in `icons.go`; provenance in `40_DEPENDENCIES.md`). Icons are `aria-hidden`; the text next to them carries the meaning.

## Charts

Dashboard:
- sparklines
- minimal labeling

Detail pages:
- clean analytical charts
- subtle grid
- clear hover values
- outage overlays
- maintenance overlays
- incident markers

Avoid heavy glow, 3D, gradients (except a soft area fill under the latency line), and excessive animation.

Built (M3-10, `js/chart.js`):
- colours come only from `--chart-line`, `--chart-grid`, `--chart-outage`, `--chart-maintenance` and `--status-*`; `chart.js` reads them with `getComputedStyle` and draws again when `data-theme` changes
- overlays are translucent bands behind the line: outage 18 %, maintenance and paused 22 %
- maximum is dashed, average solid
- no animation: uPlot draws once, so nothing needs `prefers-reduced-motion`
- monospace axis and legend text, 24-hour times

## Forms

- labels always visible
- validation near field
- advanced sections collapsed by default
- dangerous actions isolated and clearly marked
- secrets masked
- no placeholder-only labels

## Empty states

Every major surface needs a purposeful empty state:
- no monitors
- no incidents
- no status pages
- no notification profiles
- no maintenance
- no history yet

Empty states should include the most likely next action without marketing fluff.
