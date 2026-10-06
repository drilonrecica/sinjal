# Configuration, Import/Export, Backup and Restore

## Configuration split

Environment/CLI:
- how Sinjal runs

SQLite:
- what Sinjal monitors and user-managed app configuration

## Environment examples

```text
SINJAL_DATA_DIR=/data
SINJAL_LISTEN=:8080
SINJAL_BASE_URL=https://sinjal.example.com
SINJAL_TRUSTED_PROXIES=172.16.0.0/12
SINJAL_LOG_FORMAT=text
SINJAL_TIMEZONE=Europe/Belgrade
```

Do not put every monitor in environment variables.

## Import/export

Support config export/import separate from full backup.

### Safe config export
Human-readable YAML or JSON containing:
- monitor definitions
- groups/tags
- status pages
- notification profile structure
- non-secret settings

Exclude:
- passwords
- passkeys
- TOTP secret
- notification credentials
- auth headers
- heartbeat raw tokens unless intentionally regenerated

### Full backup
Contains everything required for disaster recovery:
- SQLite DB
- master key
- necessary uploaded assets
- metadata/version manifest

Full backup is sensitive.

## CLI

Target commands:

```text
sinjal version
sinjal backup <path>
sinjal restore <path>
sinjal export-config <path>
sinjal import-config <path>
sinjal check-db
sinjal healthcheck
sinjal reset-admin [--login <login>] [--remove-passkeys]
```

`sinjal healthcheck` performs `GET http://127.0.0.1:<port>/healthz` (port taken from `SINJAL_LISTEN`) with a 3 s timeout and exits 0 on HTTP 200, otherwise 1. It makes no other network calls and does not open the database. It exists for the Docker `HEALTHCHECK` in the `scratch` image.

`sinjal reset-admin` recovers a locked-out admin (decision P0-11):
- operates on the database in `SINJAL_DATA_DIR`
- `--login` is required only when more than one admin exists
- sets a new random password and prints it once to stdout
- clears TOTP
- deletes all sessions of that user
- keeps passkeys unless `--remove-passkeys` is given
- writes the audit event `admin_reset_cli`
- safe while the server is running (e.g. `docker exec <container> /sinjal reset-admin`): it performs ordinary transactional DB writes, and the server never caches authentication state in memory

Do not grow a huge CLI framework.

## Automatic local backup

Daily.

Default retention: 14 daily backups.

Path:
`/data/backups/`

Old backup cleanup must never delete outside that directory.

## Pre-migration backup

Mandatory before schema migration.

If backup fails, migration must not proceed.

## Restore

Restore is destructive and requires explicit command/admin confirmation.

Validation:
- archive manifest
- compatible format
- DB integrity where possible
- key presence for encrypted secrets

Document rollback path clearly.
