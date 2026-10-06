# Configuration, Import/Export, Backup and Restore

## Configuration split

Environment/CLI:
- how Sinjal runs

SQLite:
- what Sinjal monitors and user-managed app configuration

## Environment variables

```text
SINJAL_DATA_DIR=/data
SINJAL_LISTEN=:8080
SINJAL_BASE_URL=https://sinjal.example.com
SINJAL_TRUSTED_PROXIES=172.16.0.0/12
SINJAL_LOG_FORMAT=text
SINJAL_LOG_LEVEL=info
SINJAL_TIMEZONE=Europe/Belgrade
SINJAL_WORKERS=16
```

| Variable | Default | Rules |
|---|---|---|
| `SINJAL_DATA_DIR` | `/data` | made absolute |
| `SINJAL_LISTEN` | `:8080` | `host:port` or `:port`, numeric port 1–65535 |
| `SINJAL_BASE_URL` | unset | absolute http/https URL, no credentials/query/fragment; trailing slash trimmed. Passkeys need it: an `https` URL with a host name (or `http://localhost`), see `13_AUTH_SECURITY.md` |
| `SINJAL_TRUSTED_PROXIES` | none | comma-separated CIDRs or bare IPs; empty trusts no proxy |
| `SINJAL_LOG_FORMAT` | `text` | `text` or `json` |
| `SINJAL_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SINJAL_TIMEZONE` | `UTC` | IANA name (tz database is embedded) |
| `SINJAL_WORKERS` | auto | integer 1–256; auto is `min(32, max(8, NumCPU*4))` |

Blank values count as unset. Invalid values and unknown `SINJAL_*` variables (typos) are fatal at startup, and every problem is reported at once.

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

### Config import (decision P0-16)

Matching existing items:
- monitors, notification profiles, tags: by `name`
- notification channels: by their export `id` key (e.g. `telegram-main`)
- status pages: by `slug`

Modes:
- `skip` (default): matched items are left unchanged; unmatched items are created
- `replace`: matched items are overwritten with the imported definition; unmatched items are created
- nothing is ever deleted by an import

Preview is mandatory:
- CLI: `sinjal import-config --dry-run [--mode skip|replace] <path>` prints the plan; without `--dry-run` it applies it
- UI: upload shows the plan; applying requires explicit confirmation
- the plan lists every item as create / replace / skip / error, with reasons

Errors (the whole import is rejected):
- duplicate names/keys/slugs within the file
- a name that matches more than one existing monitor
- a reference (profile on a monitor, monitor on a status page, channel in a route) that resolves neither in the file nor in the database
- any validation failure from `38_CONFIG_VALIDATION.md`

Secrets (the safe export never contains them):
- `replace` keeps the existing secrets of a matched item
- newly created notification channels are created **disabled** and marked "needs credentials"
- newly created heartbeat monitors get fresh tokens, shown once after import
- secret-looking values in the file (any `secrets`/`token` field other than `REDACTED`) are rejected

Application:
- the file is fully parsed and validated before any write
- the plan is applied in a single transaction; on any error nothing changes
- one audit event `config_imported` records mode and counts

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
sinjal import-config [--dry-run] [--mode skip|replace] <path>
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
