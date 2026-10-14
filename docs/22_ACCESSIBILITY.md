# Accessibility

## Baseline

Aim for practical WCAG 2.2 AA behavior for normal application flows.

## Required

- keyboard operable navigation
- visible focus states
- semantic headings
- form labels
- error association
- ARIA only where native semantics are insufficient
- status never conveyed by color alone
- reduced-motion support
- adequate contrast in every theme
- charts accompanied by textual values/summary
- command palette keyboard accessible
- mobile touch targets reasonably sized

Built:
- **Charts** (M3-10, M8-16): on the History tab a summary paragraph with every figure comes before the chart. The chart is `role="img"` and points to the summary with `aria-describedby`. The availability timeline is SVG with titles. Every colour has a text key. Beat bars and day bars are `role="img"` with the bar in words.
- **Status:** colour, glyph shape and word together (`04_DESIGN_SYSTEM.md` "Status semantics").
- **Forms:** invalid fields point to their message with `aria-describedby` and `aria-invalid`.
- **Touch targets** (M8-09): 44 px with a coarse pointer (`03_INFORMATION_ARCHITECTURE.md` "Mobile").
- **Reduced motion:** `04_DESIGN_SYSTEM.md` "Motion".

## Live updates

SSE status changes should not cause disruptive screen-reader chatter.

Use polite live regions only for important user-facing status changes.

Built (M8-10): rows, headers and lists update silently. Only the stream itself is announced, politely: "Live updates are paused" after 8 s without it, and "Live updates resumed" when it is back (`32_SSE_EVENTS.md`).

## Themes

All three themes (Stark, Terminal, Daylight) must pass contrast checks for:
- body text
- muted text used for essential information
- status labels
- links
- form controls
- focus rings

Built (M8-02): `web/tokens_test.go` checks the tokens and what the components paint over them:
- text on a status badge's tint and on a notice's tint
- text inside the Ambient light at its strongest
- text on Daylight's ink block and under Terminal's scanlines
- beat marks on the page background

`TestPaintedMixesMatchCSS` fails when `components.css` changes one of those tints without the test.

## Tables/rows

Responsive monitor rows must maintain logical reading order on mobile.

Built (M8-09): a list column row reads state, name, uptime, beats, in the order it shows (`03_INFORMATION_ARCHITECTURE.md` "Mobile").

## Toasts

Toasts must:
- be dismissible when persistent
- not be the only place an error is explained
- use appropriate live-region behavior

Built (M8-10, `ToastRegion`, `js/toast.js`):
- The shell holds one toast region with two live regions that exist from page load: polite (`role="status"`) for confirmations and notes, assertive (`role="alert"`) for errors.
- Every toast has a Dismiss button; Escape closes the focused one.
- Error toasts stay until dismissed. Others leave after 6 s, but not while focus is inside them.
- A failed list column, More panel or Live activity load says so in place, with a Retry.

## Keyboard

Built (M8-06, M8-07):
- **Command palette:** Ctrl/Cmd+K, or the top bar's "Commands" button. A combobox that owns one grouped listbox: ↑/↓ (wrapping) move the active option (`aria-activedescendant`), Enter runs it, Escape closes the palette and focus returns to where it was.
- **Shortcuts** (`03_INFORMATION_ARCHITECTURE.md` "Keyboard shortcuts"): inactive while typing in a field, while a dialog is open, and with Ctrl/Cmd/Alt held.
- **List column:** ↑/↓, Home and End move between rows.
- **Beat bar** on a monitor's header: a tab stop; ←/→, Home and End move its tooltip through the beats.
