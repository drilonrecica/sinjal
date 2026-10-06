package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/vault"
)

// secretContext binds a secret to its row so a copied envelope will not open
// elsewhere. Monitor ids are hex, so "id/name" cannot collide across rows.
func secretContext(monitorID, key string) vault.Context {
	return vault.Context{Table: "monitor_secrets", Column: "value_enc", RowID: monitorID + "/" + key}
}

// SetSecret stores (or replaces) one encrypted secret of a monitor, such as
// an auth header value. Secrets are kept apart from the monitor row so list
// queries never load them.
func SetSecret(ctx context.Context, d *db.DB, key *vault.Key, monitorID, name string, value []byte, now time.Time) error {
	if name == "" {
		return &InputError{"name", "Enter a secret name."}
	}
	enc := key.Seal(secretContext(monitorID, name), value)
	return db.Retry(ctx, func() error {
		_, err := d.Writer.ExecContext(ctx, `INSERT INTO monitor_secrets (monitor_id, key, value_enc, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT (monitor_id, key) DO UPDATE SET value_enc = excluded.value_enc, updated_at = excluded.updated_at`,
			monitorID, name, enc, formatTime(now))
		return err
	})
}

// GetSecret decrypts one secret.
func GetSecret(ctx context.Context, q *sql.DB, key *vault.Key, monitorID, name string) ([]byte, error) {
	var enc []byte
	err := q.QueryRowContext(ctx, `SELECT value_enc FROM monitor_secrets WHERE monitor_id = ? AND key = ?`, monitorID, name).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return key.Open(secretContext(monitorID, name), enc)
}

// SecretNames lists the names (never values) of a monitor's secrets.
func SecretNames(ctx context.Context, q *sql.DB, monitorID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT key FROM monitor_secrets WHERE monitor_id = ? ORDER BY key`, monitorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteSecret removes one secret; a missing one is not an error.
func DeleteSecret(ctx context.Context, d *db.DB, monitorID, name string) error {
	return db.Retry(ctx, func() error {
		_, err := d.Writer.ExecContext(ctx, `DELETE FROM monitor_secrets WHERE monitor_id = ? AND key = ?`, monitorID, name)
		return err
	})
}
