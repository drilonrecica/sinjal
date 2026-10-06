package store

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/ids"
)

// MaxTagLen bounds a tag name.
const MaxTagLen = 40

// NormalizeTag trims a tag name and rejects empty, long or control-character
// names.
func NormalizeTag(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", &InputError{"tags", "Tags cannot be empty."}
	}
	if len([]rune(name)) > MaxTagLen {
		return "", &InputError{"tags", "Use at most 40 characters per tag."}
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == ',' {
			return "", &InputError{"tags", "Tags cannot contain commas or control characters."}
		}
	}
	return name, nil
}

// upsertTag returns the id of the tag with that name, creating it when
// missing. Names compare case-insensitively so "Prod" and "prod" are one tag.
func upsertTag(ctx context.Context, x execer, name string) (string, error) {
	var id string
	err := x.QueryRowContext(ctx, `SELECT id FROM tags WHERE name = ? COLLATE NOCASE`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	id = ids.New()
	if _, err := x.ExecContext(ctx, `INSERT INTO tags (id, name) VALUES (?, ?)`, id, name); err != nil {
		return "", err
	}
	return id, nil
}

// setTags makes the monitor's tag set exactly names. Tags no monitor uses
// any more are removed so the list stays tidy.
func setTags(ctx context.Context, x execer, monitorID string, names []string) error {
	seen := map[string]bool{}
	var clean []string
	for _, n := range names {
		n, err := NormalizeTag(n)
		if err != nil {
			return err
		}
		if k := strings.ToLower(n); !seen[k] {
			seen[k] = true
			clean = append(clean, n)
		}
	}
	if _, err := x.ExecContext(ctx, `DELETE FROM monitor_tags WHERE monitor_id = ?`, monitorID); err != nil {
		return err
	}
	for _, n := range clean {
		tagID, err := upsertTag(ctx, x, n)
		if err != nil {
			return err
		}
		if _, err := x.ExecContext(ctx, `INSERT INTO monitor_tags (monitor_id, tag_id) VALUES (?, ?)`, monitorID, tagID); err != nil {
			return err
		}
	}
	_, err := x.ExecContext(ctx, `DELETE FROM tags WHERE id NOT IN (SELECT tag_id FROM monitor_tags)`)
	return err
}

// SetMonitorTags replaces a monitor's tags.
func SetMonitorTags(ctx context.Context, d *db.DB, monitorID string, names []string) error {
	return db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM monitors WHERE id = ?`, monitorID).Scan(&one); err == sql.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if err := setTags(ctx, tx, monitorID, names); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// MonitorTags returns a monitor's tag names, sorted.
func MonitorTags(ctx context.Context, q *sql.DB, monitorID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.name FROM tags t JOIN monitor_tags mt ON mt.tag_id = t.id
		WHERE mt.monitor_id = ? ORDER BY t.name COLLATE NOCASE`, monitorID)
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
	sort.Strings(out)
	return out, rows.Err()
}

// ListTags returns every tag name in use, sorted.
func ListTags(ctx context.Context, q *sql.DB) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM tags ORDER BY name COLLATE NOCASE`)
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

// TagsByMonitor returns every monitor's tag names in one query, for list
// pages.
func TagsByMonitor(ctx context.Context, q *sql.DB) (map[string][]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT mt.monitor_id, t.name FROM monitor_tags mt
		JOIN tags t ON t.id = mt.tag_id ORDER BY t.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = append(out[id], n)
	}
	return out, rows.Err()
}
