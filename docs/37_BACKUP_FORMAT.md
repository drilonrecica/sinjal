# Backup Archive Format

Not built yet (M9). What a backup contains, and the restore rules, are in `15_CONFIG_BACKUP.md`.

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

Separate format: `sinjal-config.yaml`.

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
- take a current backup before replacing current data, where possible

After restore:

- verify operationally that the master key matches the encrypted data
- run required forward migrations only after a preserved restore point exists
