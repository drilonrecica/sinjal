# Status Pages

Public and private pages that show the state of selected monitors. Access control is in `13_AUTH_SECURITY.md` "Status page access"; field validation is in `38_CONFIG_VALIDATION.md` "Status page".

## Multiple pages

One Sinjal instance may host multiple independent status pages.

Examples:
- public apps
- internal services
- business-specific service page
- personal infrastructure

## Visibility modes

1. Public
2. Authenticated
3. Password-protected
4. Unlisted

### Public
Anyone can access.

### Authenticated
Requires Sinjal viewer/admin login.

### Password-protected
Separate page password. No full user account required.

### Unlisted
Tokenized/unguessable URL and no navigation/index link.

Unlisted is not a substitute for strong authentication for highly sensitive information.

URL shape (decision P0-15):
- `/s/{token}`, with `/s/{token}/api.json` and `/s/{token}/feed.xml`
- token: 128 bits from `crypto/rand`, base32 (lowercase, no padding), in the path so links survive copy/paste
- only the token hash is stored; the full URL is shown once on creation or regeneration (regenerating invalidates the old URL)
- responses send `X-Robots-Tag: noindex, nofollow` and `Referrer-Policy: no-referrer`
- the `/status/{slug}` path does not serve unlisted pages

Enforcement (M7-05) is described in `13_AUTH_SECURITY.md` "Status page access":
- authenticated: sign-in, then back to the page
- password: a password form and a page-scoped signed cookie (7 days, invalidated by a password change, rate-limited attempts)
- unlisted: the token address only, with the noindex headers above

## Content

Public defaults:
- public display name
- state
- uptime
- recent incidents
- optional latency

Do not expose by default:
- internal URL
- private hostname
- private IP
- monitor configuration
- auth details
- diagnostic response snippets

## Uptime strip

- 90 daily bars per monitor, oldest left
- each bar uses **adjusted** uptime for that day (`10_INCIDENTS.md`), in the instance timezone
- days fully covered by excluded maintenance use a distinct maintenance style and label
- days before the monitor existed, or fully paused, are neutral "no data"
- every bar has a text tooltip/label (date, uptime, incident count); color is never the only signal
- the headline uptime figure for a page uses the same 90-day adjusted value

Implementation (M7-04): `internal/web/strip.go`, shared with a monitor's Overview.
- One read of a monitor's intervals covers the 90 local days. Midnights come from the calendar, so a daylight-saving day has 23 or 25 hours. Each day is then computed in memory.
- Day classes:
  - **up**: 100 % adjusted
  - **partial**: some downtime, at or above 95 %
  - **down**: below 95 %
  - **maintenance**: the day has time, but none is left after excluded maintenance
  - **no data**: no time at all (before creation, fully paused)
- A bar's tooltip reads, for example, "Sat 28 Mar 2026: 99.30% uptime, 1 incident".
- The strip is one `role="img"` (`templates.DayBars`) whose label summarises it for assistive technology: days without incidents, with downtime, in maintenance, without data.
- The page's headline figure is the adjusted 90-day uptime of all its services together (time-weighted).

## Rendering (M7-04)

> **Replaced (SL, owner decision 2026-10-26).** The single page design below and under "Built (M8-17, Clear)" is replaced by the four layouts in "Layouts". It is not kept as a fifth layout. These rules still hold in every layout: the overall status and its words, which incidents are listed and what they never show, the refresh, the cache, the footer and the Daylight theme fallback. The parts that are replaced are marked.

`GET /status/{slug}` renders with separate public templates (`web/templates/public.templ`, `css/public.css`): no admin shell, no script, the page's own theme (never the visitor's) and accent.

Overall status, worst first; paused services are not counted:
1. **Major outage**: every service down
2. **Partial outage**: some down
3. **Degraded performance**: pending or flapping
4. **Under maintenance**: a window covers a service now
5. **All systems operational**

"No services are monitored" when nothing is counted.

Services (replaced: each layout decides what a service shows, see "Layouts"): each shows its public name, its state in words with a glyph (Operational, Down, Checking, Unstable, Not monitored, Maintenance), the 90-day strip, its 90-day adjusted uptime and, when "show latency" is on, the last check's duration.

Incidents:
- Listed: incidents of the page's services that are active or ended within `incident_days`, newest first, at most 50.
- Shown: the service's public name, when it started, how long it was (or has been) down, and its published notes.
- Never shown: the monitor's own name, its target (URL, host, IP, port), the incident summary or failure kind, check errors or response snippets, ids.

Refresh (M7-08):
- The page reloads itself every 60 s with `<meta http-equiv="refresh" content="60">`. There is no script, no HTMX, and never the admin SSE stream (`/events` needs a session and is not served on mapped hostnames).
- The password form and message pages do not refresh.
- A script-free reload keeps the public CSP free of any public script and needs no event plumbing. The 10 s view cache below keeps a minute of visitors cheap.

Cache: the figures are kept in memory for 10 s per page and shared by its visitors. Saving the page (any admin change moves `updated_at`) makes the next visit read them again. Responses are `Cache-Control: no-store`.

Footer: "Powered by sinjal" is a small low-contrast footer, removed with the page setting.

Built (M8-17, Clear), **replaced by "Layouts"** except for the last bullet, the theme fallback, which still holds:
- The page opens with its hero: the overall status as one sentence. It is the overall state's words (the strings above, unchanged for `api.json` and the feed) and, during an outage, the public names of the services that are down ("Partial outage: Checkout is down."; three names, then "and n more").
- The Ambient light of the app's heroes sits behind it: red for an outage, amber when degraded, the accent otherwise; a faint halo in Daylight, none in Terminal.
- The newest published note of an ongoing incident is shown right under the sentence (service, time, text), so visitors read the explanation first. The note box is as wide as the service list; its text keeps a readable measure. Every note also stays with its incident below.
- Services draw the shared 90-day bars (`DayBars`, as on a monitor's Overview).
- A page whose theme is unknown or empty renders in Daylight, the status page default (`templates.PublicDefaultTheme`), never Stark. New pages also start in Daylight (`statuspage.DefaultTheme`).

## Layouts

Owner decisions (2026-10-26, phase SL; `imp-docs/decisions.md` "SL").

A status page has four layouts. Each page has a `layout` setting beside its `theme`, and any layout works with any theme (Daylight, Stark, Terminal), in comfortable and compact density, on desktop and on a phone.

| Value | Name | For | One-line description (admin form) |
|---|---|---|---|
| `signal` | Signal | Visitors who only want to know "is it up?" | A large status lamp, then each service as a card. |
| `sentence` | Sentence | Small teams and personal sites; the default | The status as a plain sentence, services as a quiet list. |
| `heartline` | Heartline | Technical audiences who watch response times | A live 24-hour response pulse for each service. |
| `logbook` | Logbook | Pages whose history matters | A 90-day calendar and a logbook of every incident. |

The reference sketches are `imp-docs/status-layouts/{Signal,Sentence,Heartline,Logbook}.dc.html`. Each shows Daylight calm with example history and Stark during an outage. Follow their composition, hierarchy and copy style, but build with Sinjal's tokens, classes and templ partials, not the sketches' inline styles, runtime or script. Their data is invented. The phone compositions below have no sketch: they are specified here.

### What every layout shares

These rules hold in every layout. Shared parts are built once (SL-03) and each layout arranges them.

- **Content.** Every layout shows only what "Content" allows. The incident rules of "Rendering (M7-04)" apply unchanged: which incidents are listed (`incident_days`, at most 50, newest first) and what they never show. `api.json` and `feed.xml` do not depend on the layout.
- **No script.** The page is script-free under the same CSP and reloads itself with the meta refresh (60 s). The CSP allows one hashed `<style>` (the accent) and no `style` attributes, so the per-item placement a layout needs (calendar cells, pulse paths) uses classes and SVG attributes, never inline styles.
- **Centred column.** The content column is centred in the window at every width. Column widths: Signal and Logbook 960 px, Sentence 880 px, Heartline 1000 px, each at most the window less the gutters. On a phone (below 640 px) every layout is one column with 16 px gutters and no horizontal scroll.
- **Ambient light.** Where a layout shows it (Signal and Heartline), it fades out to nothing in every direction. It has no clipped or hard edges, it is not cut off by the column or the window, and it causes no horizontal scroll. Its colour follows the hero state, as before. Terminal draws none.
- **Header.** From left to right: the logo when one is set, the title and the description (on its own line below the title, smaller and muted; left out when empty). On the right is a **Subscribe** link with the Lucide `rss` icon to the page's `feed.xml`, built from its access base: `/status/{slug}/feed.xml`, `/s/{token}/feed.xml`, or `/feed.xml` on a mapped hostname. On a phone the link keeps its icon and word and stays on the title's row; the description wraps below. Its touch target is at least 44 px.
- **Headline.** The overall state as a short sentence, the same in the three layouts that show one (Signal, Heartline, Logbook):
  - up: "All systems operational"
  - outage (partial or major): the down services by public name, "Checkout is down" or "Checkout and API are down"; three names, then "and n more"
  - degraded: the pending and flapping services the same way, "Checkout is unstable"
  - maintenance: "Checkout is under maintenance" (the services a window covers now)
  - none: "No services are monitored"
  
  The overall words of "Rendering (M7-04)" ("Partial outage" and the others) stay in `api.json`, the feed and the page `<title>`. Sentence writes its own longer form (below).
- **Last issue line** (`LastIssue`): when nothing is wrong, the newest listed incident that has ended, in words: "The last interruption was 3 days ago, 12 minutes on Scaneks.com." ("today", "yesterday" and "n days ago" count local calendar days). Without one: "No interruptions in the last 30 days." (the page's `incident_days`).
- **Ongoing note.** During an incident, the newest published note of an ongoing incident is shown in the hero with its service and time, so visitors read the explanation first. Every note also stays with its incident further down.
- **"Monitoring since".** A service whose first day with data falls inside the 90 days does not show its no-data days. Its strip shows a dashed line with "Monitoring since 25 Oct" over those days, and bars from that day on (`PublicDayBars`). The label is drawn only over a gap of at least 40 days, where it fits on a phone; a shorter gap is the dashed line alone, and the downtime line names the day. Its downtime line reads "No downtime since 25 Oct" when it has none. Days without data later on (a full day paused) still show as no-data bars.
- **One axis.** "90 days ago … Today" appears once per page, under the last group of strips, not under every service.
- **Downtime in human time** (`Downtime`), from the adjusted 90-day ratio: "Down 26 min in 90 days", "Down 1 h 5 min in 90 days" or "No downtime in 90 days". With "Monitoring since", the span is "since 25 Oct" ("Down 12 min since 25 Oct"). While a service is down: "Down now, for 18 min". Under a minute is "under 1 min"; a service with no data at all has no downtime line.
- **States.** Every service state has its word and a glyph whose shape differs per state (the existing `publicStateIcon`); colour is never the only signal. Terminal adds its state tags.
- **Footer.** "Times in CET. The page refreshes every minute." (the instance time zone's abbreviation), and "Powered by Sinjal" unless the page turns it off. On a phone the two lines stack.
- **Touch targets** are at least 44 px. The calendar's day cells are at least 24 px, with spacing (WCAG 2.2 AA "Target size (minimum)").
- **Motion** is CSS only, and only the three marks below move: Signal's lamp breathing, Heartline's scan line and its live dot. All three stop under `prefers-reduced-motion` (`04_DESIGN_SYSTEM.md` "Status page marks").

### Signal (A)

For visitors who want one answer at a glance. Sketch: `Signal.dc.html`.

Desktop, top to bottom:
1. Header, with Subscribe as a bordered button.
2. Hero, centred, with the Ambient light behind it:
   - the **status lamp** (128 px, `04` "Status lamp") with its glyph
   - the headline (large)
   - the meta line: calm "5 services · 99.91% uptime over 90 days · Updated 00:57 CET"; outage "4 of 5 services operational · Updated 14:10 CET"
   - during an incident, the ongoing note as a card (at most 620 px wide, text left-aligned): "Update · 14:05 · Scaneks.com", then the text
3. Groups. Each group has its name and, on the right, a summary in words: "All 3 operational", or "1 of 3 down" in the down colour (also "1 of 3 unstable", "1 of 3 in maintenance"; the worst one is named).
4. Services as cards in two columns. A card holds:
   - a mini lamp (10 px), the name, then "Operational · 71 ms" (the state in its colour; latency only when shown, "no response" while down)
   - on the right, the 90-day uptime figure and "90-day uptime" under it
   - the 90-day strip (26 px high), with "Monitoring since" where it applies
   
   A down service's card has a border tinted with the down colour.
5. The single axis.
6. "Incidents": each incident has its date and time, the service, a badge ("Ongoing" filled in the down colour, "Resolved" quiet), its duration ("Down for 12 min", "Down for 18 min so far") and its notes with their times.
7. Footer, centred.

Phone (390 px): the lamp shrinks to 96 px and the headline wraps, centred. The note card takes the full column. Group summaries stay on the group's row. Cards are one column; the uptime figure stays on the right of the name. Incidents stack: date, then the service with its badge, duration and notes.

Calm: a green lamp with a check, "All systems operational" and calm group summaries. Outage: a red lamp with an x, the down services as headline, the note card, the down cards' tinted borders and "1 of 3 down". Degraded and maintenance use their own lamp colour and glyph (`04`).

Shows: the uptime figure per service and latency where shown. Leaves out: the downtime sentence per service, the calendar and the response history.

Terminal: the lamp is a square phosphor block without gradients or breathing, the cards have box rules, and states carry their tags.

Built (SL-05, `signalPage` in `web/templates/public.templ`, `css/public_signal.css`):
- `data-overall` on `.public-page` sets `--lamp`, the overall state's colour, which the lamp, its ring and its halo take; `data-state` (`HeroState`) on the hero colours the shared Ambient light behind it.
- `statusLamp` draws the lamp: a halo that breathes (4.5 s; still at its middle opacity under reduced motion), a bulb lit by radial gradients from `--lamp` and the overall state's glyph (`overallIcon`: check, `octagon-x`, `triangle-alert`, `wrench`, `circle-dashed`). The lamp is `aria-hidden`: the headline carries the words.
- The headline (`signalHeadline`) is the page's `h1`: "All systems operational", the down services with their verb ("Scaneks.com is down", the shared `Names` and `Verb`), or "No services are monitored". The meta line reads "5 services · 99.91% uptime over 90 days · Updated 00:57 CET" when calm, "4 of 5 services operational · Updated 14:10 CET" otherwise and "Updated 14:10 CET" alone with nothing monitored (`signalMeta`).
- The ongoing note is the shared `publicNote` blockquote, styled as a card at most 620 px wide with its "Update at 14:05 CET · Service" line first (CSS order), without the quote marks.
- Each group (`signalGroup`) is an `h2` with its summary on the right (`signalGroupSummary`): "1 of 3 down", "1 of 3 unstable" (pending or flapping), "1 of 3 in maintenance", the worst named and coloured; otherwise "All 3 operational", "Operational" for a single service, or "2 of 3 operational" when one is not monitored.
- Each service is a card (`signalCard`): the mini lamp (coloured by the card's `data-state`), the name, the state word with its Terminal tag and, where the page shows latency, " · 71 ms" or " · no response" while down (`signalLatency`); the uptime figure with "90-day uptime" on the right; the shared `PublicDayBars` strip at 26 px. A down card's border is tinted with the down colour.
- Incidents (`signalIncident`) under an "Incidents" `h2`: "Fri 23 Oct, 09:14" on the left; the service with an "Ongoing" badge (filled in the down colour) or "Resolved" (quiet); "Down for 12 min" or "Down for 18 min so far" from the new `PublicIncident.Short` (`shortDuration`); the notes through the shared `publicNotes` partial, which Sentence's incidents now use too.
- Phones: the lamp is 96 px, the headline wraps centred, the note card fills the column, cards are one column with the figure still on the right, and an incident's date sits above its service.
- Terminal: the bulb is a square block in `--lamp` without gradients, ring or halo; the cards and the note card have double box rules; the state word is its tag, `[DOWN]` and `[FLAP]` inverse; the badges are outlined.

### Sentence (B, the default)

For small teams and personal sites: the page reads like a short note. New pages start with it and existing pages move to it. Sketch: `Sentence.dc.html`.

Desktop, top to bottom:
1. A thin rule across the top of the page (4 px) in the overall status colour: up green, down red, degraded amber, maintenance blue, none neutral.
2. A small header: logo, title, "Status" in muted text, the description; on the right "Subscribe to updates" as a plain link with the icon.
3. The hero: a large editorial sentence (`h1`), then a smaller muted line.
   - Calm: "Everything at <title> is working." Then `LastIssue`.
   - Outage: the down services by name, "Scaneks.com **is down.**", "**are down.**" for several (three names, then "and n more"), with the verb in the down colour. Then "For 18 minutes so far. Everything else is working." The time is that of the longest ongoing outage; the second sentence is left out when every service is down. Then the newest ongoing note as a quote with a rule in the down colour, and "Update at 14:05" under it.
   - Degraded and maintenance follow the outage form: "Checkout **is unstable.**", "Checkout **is under maintenance.**" in their own colour, with "Everything else is working."
   - None: "Nothing is monitored on this page yet."
4. A meta line: calm "Updated 00:57 CET · 5 services · 99.91% over 90 days"; outage "Updated 14:10 CET · 4 of 5 services operational".
5. Groups as small-caps labels over a full rule in the text colour.
6. Each service in two lines:
   - its name, a dotted leader and its state in words and colour
   - under it, a thin 90-day strip (8 px) and, on the right, its `Downtime`
7. "The last N days" (N is `incident_days`): each incident as a sentence with its date on the left. "Scaneks.com was down for 12 minutes, from 09:14." or "Scaneks.com has been down since 13:52." Its notes follow as quotes with their times.
8. The single axis sits under the last group (small, muted). The footer comes last.

Phone (390 px): the sentence drops to about 40 px and wraps; the quote's rule stays. Each service keeps its name, leader and state on one row and wraps the name if it must; the downtime line goes under the strip, left-aligned. Incident dates go above their sentence.

Shows: the downtime per service in human time. Leaves out: latency (it stays in `api.json`), per-service uptime figures, cards and the calendar.

Terminal: everything in mono, the top rule in the state colour (amber when calm), box rules on groups and incidents, state tags in place of the coloured state word ("[DOWN]" as an inverse block).

Built (SL-04, `sentencePage` in `web/templates/public.templ`, `css/public_sentence.css`):
- The M8-17 markup and styles are gone: the `.public-hero` sentence with its glyph, the service cards with uptime and latency, the incident cards with their Ongoing/Resolved words and the note card.
- The view carries the headline in two parts, `Names` and `Verb` (`publicHeadline`), `DownFor` (the oldest ongoing incident, only during an outage), `Services` and `Operational` for the meta line, and per incident `Date` ("Today", "Yesterday", "Fri 23 Oct"), `Time` ("09:14") and `Long` ("12 minutes"). `Duration` stays for the feed.
- `data-overall` on `.public-page` sets `--overall`, the colour of the top rule, the verb and the quote's rule. The page has no Ambient light.
- The sentence is the page's `h1`; the header's title is a paragraph, so the heading order starts at the status. Groups and "The last N days" are `h2`.
- The hero note is a `blockquote` with "Update at 14:05 CET · Service" under it; an incident's notes are `<q>` quotes with their time after them. Both come from the shared partials, so every layout quotes notes the same way.
- Service states carry the word and, for Terminal, a tag (`publicStateTag`: UP, DOWN, PENDING, PAUSED, FLAP, MAINT) through the `status-text`/`status-tag` pattern of `components.css`; the word stays for assistive technology.
- The meta line reads "Updated 00:57 CET · 5 services · 99.91% over 90 days" when calm, "Updated 14:10 CET · 4 of 5 services operational" otherwise, and "Updated 14:10 CET" alone with nothing monitored. Services counts every service on the page; operational counts those that are up.
- Subscribe keeps the shared "Subscribe" word; the "Status" word after the title is drawn by CSS.

### Heartline (C)

For technical audiences who want to see how a service is responding now. Sketch: `Heartline.dc.html`.

Desktop, top to bottom:
1. Header with Subscribe as a bordered button.
2. Hero, with the Ambient light on the left:
   - on the left, a live dot and a mono line "LIVE · UPDATED 14:10 CET", then the headline. The check interval is left out: it is monitor configuration.
   - on the right, page figures in mono: "Uptime, 90 days" and "Response, median". The median is the median of the shown services' last response times; it is left out when no service shows latency or none has replied.
3. During an incident, the ongoing note as a band across the column: "14:05 UPDATE" in mono in the down colour, then the text.
4. A column legend in small mono caps: "SERVICE", "RESPONSE · 24 H AGO … NOW", "NOW".
5. One panel per group, its name in the panel's head. Each service row has:
   - on the left, the name and "Operational · 99.98% in 90 days" (the state in its colour; "Down since 13:52" while down)
   - in the middle, the **pulse** (`04` "Response pulse"), for services whose "Show latency" is on
   - on the right, the current response time in mono ("71 ms", "last check"), or "no reply" in the down colour
   - under it, a thin 90-day strip (5 px)
   
   A down row has a faint down-tinted background.
6. The single axis.
7. "Incidents": mono date and time, the service, "down 12 min, resolved" or "down 18 min, ongoing" in the down colour, then its notes.
8. Footer.

**Heartline latency rule.** The pulse is shown only for services whose "Show latency" is on. A service without it shows its 90-day strip at full height (26 px) in place of the pulse, and no response figure. The pulse data comes from `store.LatencyHistory` over the last 24 hours in 96 buckets, read when the page view is built and cached with it (10 s). There is no new storage and no new query. The pulse is drawn on the server as SVG; the scan line passes over pulses only.

Phone (390 px): the hero figures go under the headline, side by side. The column legend is left out. Each row becomes name and state, then the current figure on the right, then the pulse across the full width (48 px), then the strip.

Calm: a green live dot and calm pulses. Outage: a red live dot, the note band, and the down service's pulse breaking off: the time since it went down is drawn dashed in the down colour. Its figure reads "no reply".

Shows: 24-hour response history and the current response time. Leaves out: the calendar and the downtime sentence per service.

Terminal: the pulse is an amber line without area fill or scan line, panels have box rules, and the live dot is a square block.

### Logbook (D)

For pages whose history matters: what happened, when and what was said. Sketch: `Logbook.dc.html`.

Desktop, top to bottom:
1. Header with Subscribe as a bordered button.
2. Hero: a status disc (44 px) with the state glyph, the headline beside it, then one line under it:
   - calm: `LastIssue` and "Updated 00:57 CET"
   - outage: "Since 13:52, 18 minutes so far. The other 4 services are working. Updated 14:10 CET."
3. **Calendar card**: "The last 90 days, all services", with a legend: No issues, Some downtime, Outage, Maintenance, No data. Months are rows and days 1–31 are columns; day numbers sit above (1, 5, 10, … 30). Each cell is the worst day class over the page's services (down, then partial, maintenance, up, no data). Today has a ring. The current month's remaining days are dashed outlines. Under the grid, the **opened day** panel (below).
4. "Services" as a compact list: dot, name, group (muted), state, 90-day uptime or "Since 25 Oct, 100%", and latency where shown.
5. "Logbook": a vertical timeline of the incidents within `incident_days`, ongoing first. Each entry has a dot (down colour while ongoing, partial colour when resolved), "Fri 23 Oct · 09:14 to 09:26", a sentence ("Scaneks.com was down for 12 minutes", "Scaneks.com is down") and its notes with times. When an incident has no published note it says "No note was published."
6. Footer.

**Opened day.** A day with a listed incident is a link to an anchor on the page (`#day-2026-10-23`). Its panel opens through `:target` CSS, with no script. The panel lists that day's incidents as sentences, each with its notes, and ends with "Every other service had no issues." when that is true. Without a target, the newest day with an incident is shown. Days whose incidents are older than `incident_days` are not links. Every cell has its date and state in words for assistive technology ("Fri 23 Oct: some downtime, 1 incident").

Phone (390 px): the calendar turns: weeks are rows and weekdays (Mon … Sun) are columns, with cells of at least 24 px (about 40 px at 390 px). Each row is labelled with the week's first date. The same cells are placed by class, so there is one calendar in the markup. The legend wraps under the title. The services list puts the group and figures on a second line. The logbook keeps its timeline.

Calm: a green disc with a check, a calendar with a few amber days, and the newest incident day opened. Outage: a red disc with an x, today in the outage class and opened, and the ongoing entry first in the logbook.

Shows: the 90-day calendar for the whole page and the full incident history with notes. Leaves out: per-service strips (the calendar replaces them), the response history and the axis (the calendar has its own dates).

Terminal: square cells with box rules, the legend in mono, state tags in the services list.

### Default and migration

- `status_pages.layout` holds `signal`, `sentence`, `heartline` or `logbook` (`08_DATA_MODEL.md`, `38_CONFIG_VALIDATION.md`).
- New pages start in `sentence` (`statuspage.DefaultLayout`). Migration `011_status_page_layouts` adds the column with that default, so every existing page moves to Sentence.
- A page whose layout is unknown or empty renders in Sentence, as an unknown theme renders in Daylight.
- The admin form chooses it under Appearance, before Theme, as cards with the name and the one-line description above.
- The M8-17 markup and styles were removed when Sentence replaced them (SL-04). Each layout's styles are in `css/public_<layout>.css`, linked only on its pages; `public.css` keeps the shared chrome.

## Groups

Static ordered groups.

Example:

```text
Web
  Website
  API

Infrastructure
  Database
  Storage
```

## Branding

Per page:
- title
- description
- logo (PNG or JPEG only; see Logo files)
- accent
- theme
- layout (see "Layouts")
- show/hide "Powered by Sinjal"

Sinjal branding is slightly visible by default.

### Logo files

- formats: PNG and JPEG only; SVG is rejected (scriptable content, XSS risk); WebP is not supported in v1 (would need an extra dependency)
- max 512 KB and 1024 × 1024 px
- validated by sniffed content type and `image.DecodeConfig`, not by file extension
- stored as `/data/uploads/<random>.<png|jpg>`
- served with the correct `Content-Type` and `X-Content-Type-Options: nosniff`

Implementation (M7-03): `internal/statuspage/logo.go`, `internal/web/uploads.go`.
- The file name is 128 random bits in hex (`<32 hex>.png` or `.jpg`), created exclusively with mode 0600 in `<data dir>/uploads`.
- `GET /uploads/{name}` needs no session, since a public page shows its logo to everyone. Only names of the stored form are looked up, so no other file can be reached.
- Files are served with the type their extension names, never sniffed, and cached for good (`immutable`): a name is never reused.

## Incident history

Default public history: 30 days.

Configurable per page.

Manual incident note may be shown when explicitly published: each manual note has a `published` flag (default off); only published notes of incidents of monitors on the page are shown.

Built (M7-04): the note form on an incident has a "Show on status pages" checkbox, off by default. The choice is made when the note is written and cannot be changed later. The timeline marks published notes "On status pages". `incident_days` ranges from 1 to 365.

## Custom hostnames

Map hostnames to pages:

```text
status.example.com -> page A
internal.example.com -> page B
```

Sinjal does not manage DNS or TLS certificates.

A request whose trusted Host matches a mapping serves **only** that page's public routes:
- `/` (the page)
- `/api.json`
- `/feed.xml`
- static assets and uploads used by the page
- `/healthz`

Everything else (admin UI, `/login`, `/setup`, `/events`, `/api/v1/*`, other pages) returns 404 on mapped hostnames. Admin access uses the instance's own base URL.

Reverse proxy/Coolify/Caddy handles TLS.

Implementation (M7-06, `internal/web/hosts.go`):
- `HostRouter` runs before the route table. It takes the trusted Host (`proxy.Host`: `X-Forwarded-Host` only from a peer in `SINJAL_TRUSTED_PROXIES`, otherwise the request's own `Host`).
- It lowercases the host, drops the port, IPv6 brackets and a trailing dot, and looks it up in `status_page_hosts`: one primary-key read per request, about 14 µs (`18_PERFORMANCE.md`). The base URL's own host is never looked up.
- A mapped hostname is answered by a separate small table with only:
  - `GET|HEAD|POST /`: the page; POST is its password form, origin-checked
  - `GET|HEAD /api.json` and `/feed.xml` (M7-07)
  - `GET|HEAD /healthz`
  - `/static/*`, `/favicon.ico` and `/uploads/{name}`
- Everything else is 404, `/readyz` included.

On a mapped hostname the page's own access rules apply, with two differences (owner decisions, M7-06):
- There are no sessions there (session cookies belong to the instance's own host). So an **authenticated** page answers 303 to `SINJAL_BASE_URL/status/{slug}`, where signing in returns to it. Without a base URL it answers 403 "Sign in to Sinjal to see this page".
- An **unlisted** page is served at `/` with the same `noindex` and `no-referrer` headers: mapping a hostname to it is the admin's explicit choice. Its `/status/{slug}` path still does not serve it.

A password page's cookie has Path `/` on its hostname.

## JSON endpoint

Provide a small public representation appropriate to page visibility.

Example fields:
- page
- overall_status
- generated_at
- components/groups
- recent incidents

Do not expose internal IDs unnecessarily.

Implementation (M7-07, `internal/web/publicfeed.go`): `api.json` is built from the same cached view as the HTML page, so it shows exactly what the page shows.

Fields:
- `page{title,description}`
- `overall_status{state,text}`
- `generated_at`
- `uptime_90d`
- `components[]{key,name,group,status,latency_ms,uptime_90d}`
- `incidents[]{key,component,active,started_at,ended_at,notes[{at,message}]}`; `component` is the key of the incident's service

Keys are opaque: an HMAC-SHA256 with the page key over the kind, the page id and the id, as 16 lowercase base32 characters. They are stable for a page, different on every page and not reversible; no monitor, incident or page id is exposed.

Headers: `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`.

## RSS/Atom

Expose incident feed where page visibility permits.

For password/authenticated pages, do not create a public feed that bypasses access rules.

Implementation (M7-07): `feed.xml` is Atom, one entry per incident listed on the page. Each entry has the title "<service>: ongoing|resolved", an id from the opaque incident key, and the published notes as content.

Both formats go through the page's access code (`Public.serve`):
- public and unlisted pages are open; unlisted also sends `noindex` and `no-referrer` and exists only under its token
- a password page answers **401 with a plain line** (no form, no redirect) until the request carries its page cookie
- an authenticated page answers 401 without a session, also on a mapped hostname, where there are no sessions

The password cookie of a path-based page covers its `api.json` and `feed.xml` (same path prefix).
