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

Document required Linux capability/permission.

Do not make the whole container privileged if a narrower capability suffices.

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
