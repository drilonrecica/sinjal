# Design System

## Direction

Sinjal should look like a premium modern developer tool with precision-infrastructure influence.

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

Do not use monospace for all body text.

## Density

Two user-selectable modes:
- Comfortable — default
- Compact

Drive density through CSS tokens, not separate components.

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

## Geometry

Theme-dependent but restrained:
- typical radii: 2–10 px
- no giant pill-shaped panels
- pills acceptable for small tags/badges only

## Surfaces

Use:
- subtle borders
- restrained elevation
- clear hierarchy
- minimal decorative gradients
- theme-specific depth

## Motion

Minimal functional motion only:
- toast entrance/exit
- panel expand/collapse
- state transition highlight
- chart live update
- command palette

Respect `prefers-reduced-motion`.

## Icons

Use simple inline SVG icons. Prefer a consistent set such as Lucide, without shipping a runtime icon library.

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

Avoid heavy glow, 3D, gradients, and excessive animation.

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
