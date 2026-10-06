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

## Live updates

SSE status changes should not cause disruptive screen-reader chatter.

Use polite live regions only for important user-facing status changes.

## Themes

All four themes must pass contrast checks for:
- body text
- muted text used for essential information
- status labels
- links
- form controls
- focus rings

## Tables/rows

Responsive monitor rows must maintain logical reading order on mobile.

## Toasts

Toasts must:
- be dismissible when persistent
- not be the only place an error is explained
- use appropriate live-region behavior
