# Recommended Repository Layout

The tree below is the current layout. Packages marked "planned" arrive with their milestone.

```text
/
  cmd/
    sinjal/
      main.go       # subcommands: serve (default), version, reset-admin
      reset.go      # sinjal reset-admin

  internal/
    assets/         # embedded static files under content-hashed URLs
    audit/          # the security and activity log
    auth/
    config/         # SINJAL_* environment variables
    datadir/        # prepares the data directory
    db/
      migrations/   # numbered embedded SQL (17_MIGRATIONS_RELEASES.md)
    dispatch/
    engine/
    history/        # history ranges and bucket math
    ids/            # primary keys for TEXT id columns
    incident/       # the monitor state machine
    jobs/           # the daily internal job
    logging/
    maintenance/
    monitor/
      httpcheck/
      tcpcheck/
      icmpcheck/
      dnscheck/     # heartbeat monitors need no checker: results arrive at /api/v1/heartbeat
    notify/
    ratelimit/      # memory-bounded failure counter for setup and login
    recent/         # the last checks per monitor in memory (beat bar, Live activity)
    results/
    retention/
    scheduler/
    secret/         # a string type that never prints its value
    statuspage/     # status page rules that need no database
    store/
    vault/          # master key and encrypted envelopes
    web/
      middleware/
      proxy/        # trusted proxy handling
      sse/
    # planned (M9): backup/, system/

  web/
    embed.go        # embeds static/
    templates/      # icons are inline SVG in templates/icons.go (M8-01), not files
    static/
      css/
      js/           # first-party scripts and the vendored htmx, SSE extension, uPlot
      fonts/        # self-hosted woff2 + their OFL texts

  docs/
    brand/
    release-signing/  # allowed_signers (17_MIGRATIONS_RELEASES.md)
  spec/             # examples and the consolidated schema; not built
  scripts/          # release.sh.example until M11
  tests/
    integration/    # black-box tests against the built binary
    browser/        # chromedp suite, its own Go module (make test-browser)
  .github/
    workflows/      # check-only CI (27_MANUAL_RELEASES.md)

  Makefile
  go.mod
  go.sum
  AGENTS.md
  CLAUDE.md
  README.md
  CONTRIBUTING.md
  SECURITY.md
  LICENSE
```

## Package guidance

Avoid cyclical mega-packages. Do not force every package behind an interface.

- `store` contains concrete DB access, not abstract repository interfaces for every entity.
- `monitor` owns monitor execution semantics. The scheduler does not need protocol-specific details.
- `incident` owns state-transition and incident decisions.
- `engine` wires the scheduler, worker pool, check executors and result processor together. It is the only package that knows all of them.
- `results` is the single write path for check results.
- `retention` owns the rollup policy (tiers, cutoffs, the run loop). The SQL of one rollup step is `store/rollup.go`; the bucket math is `history/bucket.go`.
- `notify` owns channel configuration, message rendering and the senders (one file per channel type, no subpackages).
- `dispatch` owns routing, retries and delivery records. It is a sibling of `notify` rather than part of it, because `store` needs `notify` (channel configurations) and the dispatcher needs `store`.
- `web` converts domain/store data into presentation-specific view models.
