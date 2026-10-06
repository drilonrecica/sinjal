# Branding

## Name

**Sinjal**

The name is intentionally short, CLI-friendly, infrastructure-flavored, and associated with "signal" without using a generic English uptime name.

## Descriptor

**Lightweight self-hosted uptime monitoring.**

Use this in:
- README
- GitHub repository description
- documentation headers
- metadata

## Tagline

**Uptime monitoring without the overhead.**

Use this in:
- project homepage
- README hero copy
- release screenshots

## Product voice

- technical
- restrained
- precise
- not corporate
- not playful
- not mascot-driven
- not marketing-heavy

## Logo direction

No mascot.

Prefer one of:
1. standalone abstract geometric symbol + wordmark
2. wordmark with a distinctive modified letter

Conceptual source:
- state
- continuity
- availability
- signal
- transition
- observation

Avoid:
- generic ECG heartbeat line
- eye/surveillance cliché
- shield cliché
- radar cliché
- server rack cliché
- checkmark-in-circle cliché

## Logo

Chosen mark (decision P0-21): **open ring**. A continuity ring with one gap, holding a status pip in the accent color: availability, with one point of state.

Files:
- `docs/brand/sinjal-mark.svg`: the mark alone (64-unit box)
- `docs/brand/sinjal-wordmark.svg`: mark + "sinjal" in Inter SemiBold, outlined to paths (no font dependency)

Rules:
- the ring uses the foreground/text color, the pip uses the theme accent
- the standalone files carry light colors plus a `prefers-color-scheme: dark` variant; in the product, inline the paths with `currentColor` and `var(--accent)`
- minimum size 16 px; stroke stays 7/64 of the box (no hairline variant)
- wordmark is always lowercase "sinjal", tracking −0.03em
- no gradients, glows or effects; the pip is never replaced by a status color (the mark does not imply live state)

## Status-page branding

Default footer:
`Powered by Sinjal`

Behavior:
- small
- low contrast
- non-dominant
- removable per status page

A branded customer/status page must visually prioritize the monitored service, not Sinjal.

## Domains

No dedicated paid domain is required.

Expected project surfaces:
- GitHub repository
- GitHub Pages if desired
- personal project page
- personal Sinjal instance under a suitable `recica.dev` hostname

The project must not assume a commercial marketing domain.
