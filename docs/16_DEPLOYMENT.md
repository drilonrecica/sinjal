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

## Docker health

Expose:
- `/healthz`
- `/readyz`

Add Docker `HEALTHCHECK`.

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
