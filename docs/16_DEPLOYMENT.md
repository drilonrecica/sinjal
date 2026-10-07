# Deployment

## Supported modes

1. Docker — primary documented server path
2. Native binary — equally supported

No Docker Compose stack is required for a single Sinjal instance.

## Docker

One container.
One persistent volume.

Conceptual:

```yaml
services:
  sinjal:
    image: sinjal:<version>
    ports:
      - "8080:8080"
    volumes:
      - ./data:/data
```

Do not require:
- Redis
- PostgreSQL
- sidecar
- Node
- separate worker

### Image

Runtime base: `scratch` (see `spec/Dockerfile.example`).

- multi-stage build: static `CGO_ENABLED=0` binary built with `-trimpath -tags timetzdata -ldflags "-s -w"`
- CA bundle copied from the pinned build stage for outbound HTTPS checks and notifications
- timezone database embedded in the binary (`timetzdata`) so `SINJAL_TIMEZONE` works without `/usr/share/zoneinfo`
- no shell, no package manager, no curl; nothing in the base to patch
- runs as non-root UID/GID `65532:65532`
- `/data` is pre-created and owned by `65532`

Bind mounts: the host directory must be writable by UID 65532:

```bash
mkdir -p ./data && sudo chown 65532:65532 ./data
```

Named Docker volumes inherit the image's `/data` ownership automatically.

## Docker health

Expose:
- `/healthz`
- `/readyz`

Add Docker `HEALTHCHECK`. The image has no curl, so it runs `sinjal healthcheck`, which requests `/healthz` on the local listener and exits 0 (healthy) or 1.

Definitions:
- healthz: process can serve and core DB access works (`SELECT 1` on the read pool, 2 s limit)
- readyz: instance ready for normal traffic and migrations complete

Both are unauthenticated plain-text endpoints (`200 ok` / `200 ready`, `503 unhealthy` / `503 not ready`) with `Cache-Control: no-store`. They never include versions, paths or error details; failures are logged instead.

Startup order is config, data directory, database, migrations, then the listener opens, so while a long migration runs nothing is listening yet rather than `/readyz` answering 503.

## ICMP

Sinjal pings with an unprivileged ICMP datagram ("ping") socket first and falls back to a raw socket (see `06_MONITORING_ENGINE.md`). It never needs root or a privileged container. What it needs is one of:

- **ping sockets allowed for its group:** the process's group ID lies within the kernel setting `net.ipv4.ping_group_range` (two numbers, lowest and highest allowed GID). This one setting covers IPv6 too: Linux has no separate IPv6 range. It is per network namespace, so a container has its own value.
- **the raw-socket capability `CAP_NET_RAW`** actually held by the process (in its effective set, not only allowed by the runtime).

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

Podman's default (`default_sysctls` in `containers.conf`) is `net.ipv4.ping_group_range=0 0`: only group 0 may ping, so the non-root image cannot, and ICMP monitors fail with the `permission` error. Either:

- allow the image's group: `--sysctl net.ipv4.ping_group_range="0 65535"` (rootless Podman refuses ranges beyond the GIDs mapped into the container, so `0 2147483647` fails there with "Invalid argument"; `65532 65532` is the narrowest that works), or
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
- Or give only the binary the raw-socket capability: `sudo setcap cap_net_raw+ep /usr/local/bin/sinjal` (repeat after every upgrade: replacing the file drops it; a systemd unit can use `AmbientCapabilities=CAP_NET_RAW` instead).
- Do not run Sinjal as root for ICMP.

### When it is not allowed

Every check of an ICMP monitor then fails with the error kind `permission` (shown as "Permission" on the monitor's Diagnostics tab) and the message "ICMP is not permitted for this process: allow unprivileged ping (sysctl net.ipv4.ping_group_range) or grant the CAP_NET_RAW capability; see docs/16". The monitor goes DOWN for that reason, never for an unrelated one. Other monitor types are unaffected.

Sinjal remembers which socket kind works per address family after the first check, so a permission change takes effect after a restart.

## Reverse proxy

Expected behind:
- Caddy
- Traefik
- Coolify-managed proxy
- similar

TLS termination remains outside Sinjal in normal deployments.

Live updates use one long-lived response per open browser tab (`GET /events`, `32_SSE_EVENTS.md`). The proxy must pass it on unbuffered and must not cut idle connections in under 20 s. Caddy and Traefik do this without configuration; for nginx, Sinjal sends `X-Accel-Buffering: no`. Over HTTP/1.1 a browser allows about six connections per host, so serve Sinjal over HTTP/2 (any of these proxies with TLS) if many tabs stay open.

## Native binary

Goal:

```bash
SINJAL_DATA_DIR=/var/lib/sinjal ./sinjal
```

No runtime Node or asset build dependency.

## Static assets

Embed production assets into binary where practical.

## Custom domains

Custom status-page hostnames depend on reverse proxy DNS/TLS configuration.

Sinjal only maps trusted Host to configured page.

Set-up for a custom status hostname (M7-06):
1. Point the name's DNS at the reverse proxy and give the proxy a certificate for it.
2. Proxy the name to Sinjal like the main address, keeping the `Host` header (or sending it as `X-Forwarded-Host` from an address listed in `SINJAL_TRUSTED_PROXIES`).
3. Add the name under Hostnames on the status page's edit page. The page then answers at `https://<name>/`; the admin UI and every other route answer 404 there.

Set `SINJAL_BASE_URL` too: an authenticated page on a custom hostname sends its visitors there to sign in.

## Timezone

Persist timestamps UTC.

Instance timezone affects:
- display
- quiet hours
- maintenance scheduling

Default may come from environment, but user can configure instance timezone.

Today the instance time zone is `SINJAL_TIMEZONE` (default UTC). Maintenance windows repeat at their local time of day in it, across daylight-saving changes (`10_INCIDENTS.md` "Maintenance"); changing it moves recurring windows to the same local time in the new zone.
