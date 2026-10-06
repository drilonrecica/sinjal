# Themes

Sinjal ships with four first-class themes.

All themes share:
- layout
- semantic status meaning
- component structure
- accessibility requirements

They may differ in:
- background
- surfaces
- border intensity
- shadow depth
- accent
- typography emphasis
- radius
- chart treatment
- mono usage

## Carbon

Personality:
- graphite
- neutral
- precise
- infrastructure-first

Suggested direction:
- deep charcoal background
- slightly lighter surfaces
- restrained neutral borders
- cool accent
- compact visual feel
- radius ~6 px

## Paper

Personality:
- warm light
- editorial
- calm
- highly readable

Suggested direction:
- warm off-white background
- white/stone surfaces
- subtle gray/taupe borders
- low shadow
- radius ~8 px
- minimal visual chrome

## Midnight

Personality:
- deep navy-black
- premium developer tool
- slightly more depth

Suggested direction:
- near-black blue background
- deep blue raised surfaces
- subtle luminous chart lines
- restrained accent
- radius ~8 px

Do not make it neon.

## Terminal

Personality:
- terse
- technical
- utility-focused

Suggested direction:
- very dark neutral background
- lower radius ~2–4 px
- increased mono use for data
- green/amber accent influence
- flatter surfaces
- sharper separators

Do not turn the whole UI into a fake terminal.

## Token model

At minimum:

```css
--bg
--surface-1
--surface-2
--surface-3
--border
--border-strong
--text
--text-muted
--text-subtle
--accent
--accent-contrast

--status-up
--status-warning
--status-down
--status-paused
--status-pending
--status-flapping

--shadow-1
--shadow-2
--radius-sm
--radius-md
--radius-lg

--space-density-factor
--chart-grid
--chart-line
--chart-outage
--chart-maintenance
```

## Theme persistence

- authenticated user preference persisted server-side
- may additionally use local storage for fast pre-hydration to avoid flash
- status pages select their own theme independently

## Customization

V1 does not include a full theme editor.

Status pages may allow:
- accent override
- logo
- title
- description

Admin themes remain built-in.
