# Deployment

## Supported modes

1. Docker — the primary documented server path.
2. Native binary — equally supported.

No Docker Compose stack is required for a single Sinjal instance.

## Docker

One container. One persistent volume.

Until released images exist, build the image from the repository (`Dockerfile` at its root):

```bash
docker build -t sinjal .
docker run -d --name sinjal -p 8080:8080 -v sinjal-data:/data \
  -e SINJAL_BASE_URL=https://sinjal.example.com sinjal
docker logs sinjal   # the one-time "Initial setup: …/setup?token=…" link
```

The same with Compose: `docker-compose.yaml` at the repository root (uncomment its `ports` outside Coolify).

Do not require:

- Redis
- PostgreSQL
- sidecar
- Node
- separate worker

### Image

Built (`Dockerfile`, pulled forward from M10-07 on 2026-10-15): 18.9 MB. `.dockerignore` keeps the build context to the Go sources and `web/`.

Runtime base: `scratch`.

- multi-stage build: static `CGO_ENABLED=0` binary built with `-trimpath -ldflags "-s -w -X main.version=$VERSION"` (`--build-arg VERSION=…`, default `dev`)
- CA bundle copied from the pinned build stage, for outbound HTTPS checks and notifications
- timezone database embedded in the binary (`cmd/sinjal` imports `time/tzdata`), so `SINJAL_TIMEZONE` works without `/usr/share/zoneinfo`
- no shell, no package manager, no curl; nothing in the base to patch
- runs as non-root UID/GID `65532:65532`
- `/data` is pre-created and owned by `65532`

Bind mounts: the host directory must be writable by UID 65532:

```bash
mkdir -p ./data && sudo chown 65532:65532 ./data
```

Named Docker volumes inherit the image's `/data` ownership automatically.

## Docker health

Expose `/healthz` and `/readyz`.

Add a Docker `HEALTHCHECK`. The image has no curl, so it runs `sinjal healthcheck` (`15_CONFIG_BACKUP.md` "CLI"), which requests `/healthz` on the local listener and exits 0 (healthy) or 1. Built: the `Dockerfile` checks every 30 s (5 s timeout, 10 s start period, 3 retries). Podman keeps it only in Docker format (`podman build --format docker`).

Definitions:

- `healthz`: the process can serve and core DB access works (`SELECT 1` on the read pool, 2 s limit).
- `readyz`: the instance is ready for normal traffic and migrations are complete.

Built behaviour (`internal/web/health.go`):

- Both are unauthenticated plain-text endpoints with `Cache-Control: no-store`: `200 ok` / `503 unhealthy` and `200 ready` / `503 not ready`.
- They never include versions, paths or error details; failures are logged instead.
- Startup order is config, data directory, database, migrations, then the listener opens. While a long migration runs, nothing is listening yet, rather than `/readyz` answering 503.

## ICMP

Sinjal pings with an unprivileged ICMP datagram ("ping") socket first and falls back to a raw socket (see `06_MONITORING_ENGINE.md`). It never needs root or a privileged container. It needs one of:

- **Ping sockets allowed for its group.** The process's group ID lies within the kernel setting `net.ipv4.ping_group_range` (two numbers: the lowest and highest allowed GID). This one setting covers IPv6 too; Linux has no separate IPv6 range. It is per network namespace, so a container has its own value.
- **The raw-socket capability `CAP_NET_RAW`**, actually held by the process (in its effective set, not only allowed by the runtime).

The image runs as `65532:65532`, so for ping sockets the range must include GID 65532. A range of `1 0` (lowest above highest) allows nobody.

### Docker

- Docker Engine 20.10 and later set `net.ipv4.ping_group_range=0 2147483647` in every container by default, so ICMP monitors work in the default image with no extra flags.
- If yours does not (an older engine or a hardened runtime), allow ping sockets for the container:

  ```yaml
  sysctls:
    net.ipv4.ping_group_range: "0 2147483647"
  ```

  or on the command line: `docker run --sysctl net.ipv4.ping_group_range="0 2147483647" …`

- Prefer the sysctl over `cap_add: [NET_RAW]`. Whether an added capability reaches a non-root process depends on the runtime (Podman passes it on; see below); check before relying on it.
- Never run the container with `privileged: true` or as root for ICMP.

### Podman

Podman's default (`default_sysctls` in `containers.conf`) is `net.ipv4.ping_group_range=0 0`. Only group 0 may ping, so the non-root image cannot, and ICMP monitors fail with the `permission` error. Either:

- allow the image's group: `--sysctl net.ipv4.ping_group_range="0 65535"`. Rootless Podman refuses ranges beyond the GIDs mapped into the container, so `0 2147483647` fails there with "Invalid argument"; `65532 65532` is the narrowest range that works. Or
- add the capability: `--cap-add NET_RAW` (Podman makes it effective for the non-root user).

Checked with rootless Podman 5 and crun: default denied; the sysctl with `0 65535` or `65532 65532` allows ping sockets; `--cap-add NET_RAW` allows raw sockets.

### Kubernetes

`net.ipv4.ping_group_range` is one of Kubernetes' "safe" sysctls, so a pod may set it without changing the kubelet:

```yaml
securityContext:
  sysctls:
    - name: net.ipv4.ping_group_range
      value: "0 2147483647"
```

### Native binary (Linux)

- Check the current range: `sysctl net.ipv4.ping_group_range`. Many systemd-based distributions already ship `0 2147483647` (systemd's `50-default.conf`); a range of `1 0` means ping sockets are off.
- To allow them for every group, persistently:

  ```bash
  echo 'net.ipv4.ping_group_range = 0 2147483647' | sudo tee /etc/sysctl.d/90-sinjal-ping.conf
  sudo sysctl --system
  ```

  or allow only Sinjal's group (e.g. `= 970 970` for a `sinjal` group with GID 970).
- Or give only the binary the raw-socket capability: `sudo setcap cap_net_raw+ep /usr/local/bin/sinjal`. Repeat this after every upgrade, because replacing the file drops it; a systemd unit can use `AmbientCapabilities=CAP_NET_RAW` instead.
- Do not run Sinjal as root for ICMP.

### When it is not allowed

- Every check of an ICMP monitor fails with the error kind `permission` (shown as "Permission" on the monitor's Diagnostics tab).
- The message is "ICMP is not permitted for this process: allow unprivileged ping (sysctl net.ipv4.ping_group_range) or grant the CAP_NET_RAW capability; see docs/16".
- The monitor goes DOWN for that reason, never for an unrelated one. Other monitor types are unaffected.

Sinjal remembers which socket kind works per address family after the first check, so a permission change takes effect after a restart.

## Coolify

### Docker Compose build pack (recommended)

`docker-compose.yaml` at the repository root is written for it. Create a resource from this repository with the **Docker Compose** build pack (compose file `/docker-compose.yaml`) and deploy. Coolify sets up the rest from the file:

- **Domain and port:** `SERVICE_URL_SINJAL_8080` makes Coolify generate a domain for the `sinjal` service and route it to port 8080. Replace the generated domain with your own in the service's settings. The variable's value is not passed to Sinjal: it keeps the first generated domain, with `:8080` appended, even after the domain is changed.
- **Environment variables,** each editable later in Coolify's UI, with these defaults:
  - `SINJAL_BASE_URL`: empty. **Set it** to the domain the service is served at, e.g. `https://sinjal.example.com`, and redeploy. Without it the setup link reads `http://localhost:8080/setup?token=…` (use the token with your domain) and passkeys are off.
  - `SINJAL_TRUSTED_PROXIES`: `10.0.0.0/8,172.16.0.0/12,192.168.0.0/16`. The container publishes no port, so only Coolify's proxy reaches it; narrow it to the proxy network (`docker network inspect coolify`) if you like.
  - `SINJAL_TIMEZONE`: `UTC`
  - `SINJAL_LOG_FORMAT`: `text`, `SINJAL_LOG_LEVEL`: `info`
  - `SINJAL_WORKERS`: empty, which means 4 × CPUs between 8 and 32
- **Storage:** the `sinjal-data` volume at `/data` appears under Persistent Storage. It is a named volume, so the non-root user owns it.
- **Health check:** the file's `healthcheck` (`/sinjal healthcheck`) is what Coolify uses.

First start: the service's runtime logs (Logs, not Deployments) show `Initial setup: https://…/setup?token=…`. Open it to create the admin. Until an admin exists, every restart prints a new token.

### Dockerfile build pack

Without the Compose file, set the same by hand:

1. Build pack **Dockerfile**, port **8080**.
2. Storage: a **volume mount** (a named volume) at `/data`. Not a bind mount of a host directory: that is owned by root, and the container runs as 65532.
3. Environment: `SINJAL_BASE_URL` (the domain Coolify serves, e.g. `https://sinjal.example.com`), `SINJAL_TRUSTED_PROXIES` (the proxy's network), optionally `SINJAL_TIMEZONE` and `SINJAL_LOG_FORMAT=json`.
4. Health check: the image's own `HEALTHCHECK` is enough. Leave Coolify's health check off: it runs curl or wget inside the container, and the image has neither.
5. Domain: set it on the resource; Traefik terminates TLS and passes the event stream through unbuffered.

### Either way

A status page on its own hostname needs that hostname added to the same resource ("Custom domains" below). Back up the `/data` volume, `master.key` with it, until Sinjal's own backups exist (M9).

## Reverse proxy

Expected behind:

- Caddy
- Traefik
- Coolify-managed proxy
- similar

TLS termination remains outside Sinjal in normal deployments.

Live updates use one long-lived response per open browser tab (`GET /events`, `32_SSE_EVENTS.md`):

- The proxy must pass it on unbuffered and must not cut idle connections in under 20 s.
- Caddy and Traefik do this without configuration. For nginx, Sinjal sends `X-Accel-Buffering: no`.
- Over HTTP/1.1 a browser allows about six connections per host. Serve Sinjal over HTTP/2 (any of these proxies with TLS) if many tabs stay open.

## Native binary

Goal:

```bash
SINJAL_DATA_DIR=/var/lib/sinjal ./sinjal
```

`serve` is the default command, so this starts the server.

No runtime Node or asset build dependency.

## Static assets

Embed production assets into the binary where practical. Built: everything under `web/static` is embedded and served under content-hashed URLs (`internal/assets`).

## Custom domains

Custom status-page hostnames depend on the reverse proxy's DNS/TLS configuration.

Sinjal only maps a trusted Host to the configured page.

Set-up for a custom status hostname (M7-06):

1. Point the name's DNS at the reverse proxy and give the proxy a certificate for it.
2. Proxy the name to Sinjal like the main address, keeping the `Host` header (or sending it as `X-Forwarded-Host` from an address listed in `SINJAL_TRUSTED_PROXIES`).
3. Add the name under Hostnames on the status page's edit page. The page then answers at `https://<name>/`; the admin UI and every other route answer 404 there.

Set `SINJAL_BASE_URL` too: an authenticated page on a custom hostname sends its visitors there to sign in.

## Timezone

Persist timestamps in UTC.

The instance timezone affects:

- display
- quiet hours
- maintenance scheduling

The default may come from the environment, but the user can configure the instance timezone.

Today the instance time zone is `SINJAL_TIMEZONE` (default UTC). Maintenance windows repeat at their local time of day in it, across daylight-saving changes (`10_INCIDENTS.md` "Maintenance"). Changing it moves recurring windows to the same local time in the new zone.
