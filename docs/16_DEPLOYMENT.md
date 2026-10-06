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
- healthz: process can serve and core DB access works
- readyz: instance ready for normal traffic and migrations complete

## ICMP

Sinjal pings with unprivileged ICMP datagram sockets first and falls back to raw sockets (see `06_MONITORING_ENGINE.md`).

Docker:
- Docker Engine 20.10+ sets `net.ipv4.ping_group_range=0 2147483647` inside containers by default, so ICMP monitors work in the default non-root image with no extra flags.
- If a runtime does not set it, either allow ping sockets:

  ```yaml
  sysctls:
    net.ipv4.ping_group_range: "0 2147483647"
  ```

  or grant only the raw-socket capability:

  ```yaml
  cap_add:
    - NET_RAW
  ```

- Never run the container with `privileged: true` for ICMP.

Native binary (Linux):
- most systemd-based distributions already set a permissive `net.ipv4.ping_group_range`; check with `sysctl net.ipv4.ping_group_range`
- otherwise set it (e.g. `/etc/sysctl.d/90-sinjal-ping.conf`), or grant `sudo setcap cap_net_raw+ep ./sinjal`
- do not run Sinjal as root for ICMP

If neither is available, ICMP checks fail with an explicit `permission` error rather than reporting the target DOWN for an unrelated reason.

## Reverse proxy

Expected behind:
- Caddy
- Traefik
- Coolify-managed proxy
- similar

TLS termination remains outside Sinjal in normal deployments.

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

## Timezone

Persist timestamps UTC.

Instance timezone affects:
- display
- quiet hours
- maintenance scheduling

Default may come from environment, but user can configure instance timezone.
