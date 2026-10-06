# Backup Archive Format

## Full backup

Recommended archive structure:

```text
sinjal-backup-<timestamp>.zip
  manifest.json
  sinjal.db
  master.key
  uploads/
```

## Manifest

Example:

```json
{
  "format": "sinjal-full-backup",
  "format_version": 1,
  "sinjal_version": "0.3.0",
  "created_at": "2026-10-06T12:00:00Z",
  "database_file": "sinjal.db",
  "master_key_file": "master.key"
}
```

## Safe config export

Separate format:

```text
sinjal-config.yaml
```

Does not contain:
- master key
- password hash
- passkeys
- TOTP secret
- notification credentials
- monitor secrets
- raw heartbeat tokens

## Restore safety

Before restore:
- validate archive file names
- reject absolute paths
- reject `..`
- reject unexpected symlink entries
- validate manifest version
- check DB integrity if possible
- require explicit confirmation
- stop scheduler/writes
- take current backup before replacing current data where possible

After restore:
- verify master key matches encrypted data operationally
- run required forward migrations only after preserved restore point exists
