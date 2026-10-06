<h1><img src="docs/brand/sinjal-wordmark.svg" alt="Sinjal" width="224" height="72"></h1>

**Lightweight self-hosted uptime monitoring.**  
**Tagline:** Uptime monitoring without the overhead.

Sinjal is a deliberately small, fast, visually polished, self-hosted uptime monitor intended first and foremost for a single technical owner. It is open source, author-driven, and intentionally narrower than Uptime Kuma, Grafana, Prometheus, Datadog, or full observability platforms.

This repository package is the authoritative implementation specification for Claude Code and human development.

## Product in one sentence

A single-binary, single-container Go application using SQLite, templ, HTMX, SSE, and minimal JavaScript to monitor HTTP(S), TCP, ICMP, DNS, and heartbeat endpoints with reliable incident semantics, notifications, history, public/private status pages, excellent themes, backups, and low resource usage.

## Monitor types (draft)

> Draft section: the full README arrives with the release work (M11).

| Type | Checks | Up when |
|---|---|---|
| HTTP(S) | a URL, with method, headers, authentication and body | the status matches (default 200–399) and every body and JSON assertion holds |
| TCP port | `host:port` | a connection opens within the timeout |
| Ping (ICMP) | a host name or IP address, IPv4 or IPv6 | an echo reply comes back within the timeout |
| DNS | A, AAAA, CNAME, MX, TXT or NS records of a name, from the system's resolvers or a chosen one | records exist, and contain all (or any) of the expected values when given |
| Heartbeat | nothing: your job calls Sinjal | a beat arrives at least every expected interval plus grace |

All types share the same rules: checks every 30 s by default, a confirmation retry 5 s after a first failure, DOWN after two failures in a row, UP again after one success.

A heartbeat monitor shows its push URL once, when it is created (or regenerated, which needs a recent sign-in). Send a beat at the end of the job:

```bash
curl -fsS -m 10 -X POST https://sinjal.example.com/api/v1/heartbeat/<token>
```

### Ping permissions

Sinjal pings without root. It needs the kernel's unprivileged ping sockets allowed for its group (`net.ipv4.ping_group_range`) or the `CAP_NET_RAW` capability. Docker 20.10+ and most systemd distributions allow ping sockets out of the box; Podman and some hardened runtimes do not. Without either, ping monitors report a clear "permission" error instead of a false outage. Exact settings for Docker, Podman, Kubernetes and the native binary: [`docs/16_DEPLOYMENT.md`](docs/16_DEPLOYMENT.md#icmp).

## Hard product goals

- One Go process.
- One persistent `/data` directory.
- SQLite only.
- No Redis, PostgreSQL, message broker, external queue, Node runtime, or frontend build server in production.
- Very low idle resource usage.
- Excellent visual design with four first-class themes.
- Reliable outage detection with retries, flapping control, maintenance windows, dependencies, and durable incidents.
- Good mobile read/reaction experience.
- Multiple status pages with public, authenticated, password-protected, or unlisted visibility.
- Safe backups and migrations.
- Author-driven open-source model: issues welcome; pull requests not accepted.

## Performance budgets

- Idle RAM target: **<50 MB**
- Typical idle RAM aspiration: **<35 MB**
- Cold start target: **<1 second**
- Dashboard HTML response target on localhost: **<50 ms** under normal load
- Frontend JS target: **<100 KB compressed total**
- Container image target: **<50 MB**
- Idle CPU: effectively ~0%
- 100 monitors: trivial target
- 1,000 monitors: explicitly benchmark and support
- 10,000 monitors: not a design target

## Read first

Claude Code must read these files before implementation:

1. `AGENTS.md`
2. `CLAUDE.md`
3. `docs/00_MASTER_SPEC.md`
4. `docs/23_IMPLEMENTATION_PLAN.md`
5. `docs/24_ACCEPTANCE_CRITERIA.md`

The documents in `docs/` are normative. If code conflicts with the documents, the documents win unless explicitly amended by the project owner.
