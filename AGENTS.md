# Sinjal Engineering Constitution

This file is mandatory for every coding agent working on Sinjal.

## 1. Core rule

**Do not invent product requirements. Do not add architecture for hypothetical future needs.**

Implement only what is specified. When there is ambiguity, choose the smallest design that satisfies the existing specification.

## 2. Forbidden speculative architecture

Do not add any of the following unless the specification is explicitly amended:

- ORM
- generic repository interfaces everywhere
- dependency-injection framework
- service locator
- event bus
- message broker
- Redis
- PostgreSQL or MySQL
- external job queue
- plugin framework
- extension marketplace
- microservices
- GraphQL
- frontend SPA framework
- Node.js production runtime
- WebSockets where SSE is sufficient
- Kubernetes-specific runtime integration
- Prometheus server/scraping subsystem
- OpenTelemetry stack
- APM
- browser automation / Playwright / Chromium
- distributed probe agents
- OAuth/OIDC/SAML
- generic policy/RBAC framework
- "future proof" abstractions without a current concrete caller

## 3. Preferred implementation style

- Concrete Go packages over abstract layering.
- Standard library where it is clear and sufficient.
- Small dependencies only when they materially reduce complexity or security risk.
- Handwritten SQL.
- Explicit state machines.
- Explicit error handling.
- Bounded concurrency.
- Context deadlines on all external I/O.
- Server-rendered HTML by default.
- HTMX for partial interactions.
- SSE for server-to-browser status updates.
- Vanilla JavaScript only where necessary.
- CSS tokens for theming.
- Accessible markup and keyboard behavior.

## 4. Dependency policy

Every new dependency must answer:

1. What concrete problem does it solve?
2. Why is the standard library or existing dependency insufficient?
3. What is its runtime and binary-size cost?
4. Does it bring transitive dependencies?
5. Is the feature worth the maintenance cost?

Current preferred dependency categories:

- `github.com/go-chi/chi/v5` — routing
- `modernc.org/sqlite` — pure-Go SQLite
- `github.com/a-h/templ` — templates
- `golang.org/x/crypto` — password hashing/crypto utilities as required
- a focused WebAuthn/passkey library
- a small TOTP library if needed
- a tiny chart library such as uPlot, vendored/bundled for the web UI
- minimal parsing/helper libraries only when clearly justified

Do not add a frontend framework, CSS framework runtime, or generic component kit that causes the interface to look like a stock dashboard.

## 5. Performance is a requirement

The budgets in `docs/18_PERFORMANCE.md` are product requirements, not post-launch optimization ideas.

Before accepting a change to a hot path, consider:

- allocations
- goroutine count
- DB writes
- DB round trips
- network connection reuse
- frontend payload size
- container/binary size
- startup work

## 6. State correctness beats cleverness

Critical flows must be modeled and tested explicitly:

- healthy -> failure -> retry -> down
- down -> success -> recovered
- repeated transitions -> flapping
- maintenance suppression
- parent down -> child notification suppression
- restart during active incident
- notification retry and failure
- backup-before-migration
- history aggregation and deletion

Do not bury these behaviors in generic callback/event abstractions.

## 7. Database rules

- SQLite only.
- WAL mode.
- sensible pragmas and indexes.
- one clear write path for high-frequency check results.
- migrations are numbered embedded SQL files.
- automatic backup before schema migration.
- never mutate old migration files after release.
- migrations are forward-only; rollback uses the pre-upgrade backup.
- raw check retention and rollups are mandatory.

## 8. Security rules

- Passwords use Argon2id or a comparably strong approved KDF.
- Secrets are encrypted at rest.
- Never log secrets.
- Never render secrets back to the browser unless explicitly required by a re-authenticated action.
- CSRF protection for state-changing browser requests.
- Secure, HttpOnly session cookies.
- SameSite protection.
- login rate limiting.
- security headers.
- trusted proxy behavior must be explicit.
- status pages never reveal internal targets by default.
- viewer accounts cannot create/modify monitors.

## 9. UI rules

Sinjal must not look like a generic generated Tailwind dashboard.

Avoid:

- endless identical rounded cards
- huge empty spacing
- decorative gradient blobs
- oversized marketing typography in admin UI
- color-only state indicators
- excessive animation
- modal-heavy workflows
- "glassmorphism" everywhere

Prefer:

- strong hierarchy
- compact, information-rich rows
- precise typography
- subtle depth
- clear state labels
- proper empty/loading/error states
- responsive mobile inspection experience
- four genuinely distinct themes sharing one layout system

## 10. Testing rule

Do not mark a milestone complete because it "works manually."

Required testing categories are in `docs/20_TESTING.md`.

At minimum, any change to scheduler/state/incident logic needs unit tests and integration tests.

## 11. Scope rule

If a requested implementation seems to require a prohibited subsystem, stop and re-check the specs. The likely answer is that the feature should be implemented more simply.

## 12. Code deletion is acceptable

Prefer deleting unnecessary code over keeping abstractions "in case we need them later."

## 13. Documentation synchronization

When behavior changes, update the relevant spec and user documentation in the same change.

## 14. No surprise feature additions

Do not silently add:
- telemetry
- analytics
- external calls
- update checks
- automatic releases
- cloud services
- crash reporting
- third-party tracking

Any outbound call must exist because of a configured monitor, notification channel, explicit update-check action, or clearly specified functionality.
