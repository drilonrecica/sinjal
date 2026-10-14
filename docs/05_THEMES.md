# Themes

Sinjal ships three first-class themes (owner decision 2026-10-14, after the "Clear" design concepts). They replace Carbon, Paper, Midnight and the old Terminal.

| Theme | Kind | Key | Default for |
|---|---|---|---|
| Stark | dark | `stark` | the admin UI |
| Terminal | dark | `terminal` | — |
| Daylight | light | `daylight` | status pages |

All themes share:
- layout
- semantic status meaning
- component structure
- accessibility requirements (AA contrast, status as colour + shape + text)
- motion

They differ in character, set through tokens and a few theme-scoped rules, never through separate components.

## Stark

Personality: stark, high-contrast, dramatic, precise.

- true black background (`#000`), near-black surfaces, hairline grey borders
- near-white text; one vivid electric-blue accent (`#3d93ff`)
- vivid status colours: green means up and nothing else, red means down
- large heavy numerals, filled status blocks
- **Ambient light** (on by default, a per-user setting): one soft radial light behind the status hero. Its colour follows the state: blue when healthy, red when down, amber when flapping. It is the only decorative gradient in the product besides chart area fills (`04_DESIGN_SYSTEM.md` "Ambient light"). Switched off, Stark is pure black.

## Terminal

Personality: a warm amber phosphor terminal. Calm is one colour; only problems break it.

- warm near-black background, amber text, a pale amber accent
- the whole UI in JetBrains Mono (this theme alone sets body text in mono)
- square corners (2–4 px radii), box-rule borders, a block cursor in headings
- faint static scanlines; none with `prefers-reduced-motion`; never animated
- state tags in text: `[UP]` stays in the base amber; problems invert: `[DOWN]` is a red-orange inverse block, `[FLAP]` a pale amber inverse block
- no glow except the cursor, no neon

## Daylight

Personality: crisp, bright, editorial contrast.

- pure white background and surfaces, near-black type
- filled near-black status blocks and primary buttons (the accent is ink, not a hue)
- status colours darkened for AA on white; beat bars use brighter beat-only tones (3:1, they are marks, not text)
- the Ambient light setting shows as a faint state-tinted halo behind the status

The default for public status pages: a page's visitors are not its operators.

## Built

M8-01, M8-02, M8-17. Every rule above is in `css/tokens.css` and the theme-scoped sections of `css/components.css`.

- **Terminal's short tag.** Where the tag is not the word itself (`[FLAP]`), it is a separate `aria-hidden` span next to the word. Terminal alone hides the word visually, so assistive technology still reads "Flapping".
- **Terminal's status hero.** A box rule in the base amber for a calm state; an inverse block only for a problem.
- **Terminal's box rules.** Double rules frame the panels: incident, maintenance and channel lists, status page monitors, the problem strip, form sections and the sign-in panel. Controls keep single rules.
- **Public status page** (M8-17). It shares the hero's Ambient light and the day bars and, in Terminal, the double box rules on its service list, incidents and note. An unknown or empty page theme falls back to Daylight.

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

--on-status        /* text on a filled status block */
--beat-none        /* a beat with no result (paused, before the first check) */
--ambient-up       /* Ambient light colours per state */
--ambient-down
--ambient-flapping

--space-density-factor
--chart-grid
--chart-line
--chart-outage
--chart-maintenance
```

## Theme persistence

- authenticated user preference persisted server-side (theme and Ambient light, `users.theme`, `users.ambient_light`)
- may additionally use local storage for fast pre-hydration to avoid flash
- status pages select their own theme independently (default `daylight`)
- stored values from before `009_themes` map carbon/midnight → stark, paper → daylight, terminal → terminal

Built (M8-04):
- Settings → Appearance (`/account/appearance`) is open to every signed-in user, without re-authentication. It saves theme, density and Ambient light into the user's row (`users.theme`, `users.density`, `users.ambient_light`; a `NULL` theme is the default, Stark).
- The server renders them on every page as `data-theme`, `data-density` and `data-ambient` on `<html>`.
- The one inline script, `templates.PrehydrateScript` (admitted by its hash in the CSP, `13_AUTH_SECURITY.md`), stores them in `localStorage` key `sinjal.prefs` on signed-in pages. On pages without a signed-in user (sign-in, setup) it applies them before first paint.
- The command palette's Theme commands save the theme the same way.
- Public status pages run no script and use their own theme.

## Customization

V1 does not include a full theme editor.

Status pages may allow:
- accent override
- logo
- title
- description

Admin themes remain built-in.
