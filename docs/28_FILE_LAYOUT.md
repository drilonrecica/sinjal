# Recommended Repository Layout

```text
/
  cmd/
    sinjal/
      main.go

  internal/
    app/
    auth/
    backup/
    config/
    db/
      migrations/
    engine/
    incident/
    maintenance/
    monitor/
      httpcheck/
      tcpcheck/
      icmpcheck/
      dnscheck/
      heartbeat/
    notify/
      smtp/
      telegram/
      discord/
      webhook/
    results/
    scheduler/
    statuspage/
    store/
    system/
    web/
      handlers/
      middleware/
      sse/
      viewmodels/

  web/
    templates/
    static/
      css/
      js/
      icons/

  docs/
  spec/
  scripts/
  tests/

  AGENTS.md
  CLAUDE.md
  README.md
  CONTRIBUTING.md
  SECURITY.md
  LICENSE
```

## Package guidance

Avoid cyclical mega-packages.

`store` should contain concrete DB access, not abstract repository interfaces for every entity.

`monitor` owns monitor execution semantics; scheduler does not need protocol-specific details.

`incident` owns state-transition/incident decisions.

`engine` wires scheduler, worker pool, check executors and result processor together and is the only package that knows all of them; `results` is the single write path for check results.

`notify` owns delivery/channel behavior.

`web` converts domain/store data into presentation-specific view models.

Do not force every package behind an interface.
