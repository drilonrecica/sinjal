package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/ids"
	"github.com/drilonrecica/sinjal/internal/notify"
	"github.com/drilonrecica/sinjal/internal/vault"
)

// Channel is one notification channel without its configuration, which is
// encrypted and only read through GetChannel.
type Channel struct {
	ID            string
	Name          string
	Type          string
	Enabled       bool
	HealthState   string // unknown, healthy, warning or failed (docs/11)
	LastSuccessAt *time.Time
	LastFailureAt *time.Time
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ChannelInput is a channel to create or update. The type comes from the
// Config and cannot change once stored.
type ChannelInput struct {
	Name    string
	Enabled bool
	Config  notify.Config
}

// channelContext binds a stored configuration to its channel, so an
// envelope copied onto another row will not open (docs/13).
func channelContext(id string) vault.Context {
	return vault.Context{Table: "notification_channels", Column: "config_enc", RowID: id}
}

// validate returns the field errors of in, nil when it is acceptable. The
// configuration has already been merged with the stored secrets.
func (in ChannelInput) validate() FieldErrors {
	errs := FieldErrors{}
	name := strings.TrimSpace(in.Name)
	switch {
	case name == "":
		errs.add("name", "Enter a name.")
	case len([]rune(name)) > MaxNameLen:
		errs.add("name", fmt.Sprintf("Use at most %d characters.", MaxNameLen))
	}
	for field, msg := range in.Config.Validate() {
		errs.add(field, msg)
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

const channelColumns = `id, name, type, enabled, health_state, last_success_at, last_failure_at, last_error, created_at, updated_at`

// scanChannel reads channelColumns, then any extra columns into extra.
func scanChannel(r scanner, extra ...any) (Channel, error) {
	var c Channel
	var ok, fail, lastErr sql.NullString
	var created, updated string
	dest := append([]any{&c.ID, &c.Name, &c.Type, &c.Enabled, &c.HealthState, &ok, &fail, &lastErr, &created, &updated}, extra...)
	if err := r.Scan(dest...); err != nil {
		return c, err
	}
	if ok.Valid {
		t := parseTime(ok.String)
		c.LastSuccessAt = &t
	}
	if fail.Valid {
		t := parseTime(fail.String)
		c.LastFailureAt = &t
	}
	c.LastError = lastErr.String
	c.CreatedAt, c.UpdatedAt = parseTime(created), parseTime(updated)
	return c, nil
}

// ListChannels returns every channel by name. It never decrypts anything.
func ListChannels(ctx context.Context, q querier) ([]Channel, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+channelColumns+` FROM notification_channels ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChannel returns one channel with its decrypted configuration, or
// ErrNotFound. Use GetChannelInfo when the configuration is not needed.
func GetChannel(ctx context.Context, q querier, key *vault.Key, id string) (Channel, notify.Config, error) {
	var enc []byte
	c, err := scanChannel(q.QueryRowContext(ctx, `SELECT `+channelColumns+`, config_enc FROM notification_channels WHERE id = ?`, id), &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil, ErrNotFound
	}
	if err != nil {
		return c, nil, err
	}
	plain, err := key.Open(channelContext(id), enc)
	if err != nil {
		return c, nil, fmt.Errorf("channel %s: %w", id, err)
	}
	cfg, err := notify.Unmarshal(c.Type, plain)
	if err != nil {
		return c, nil, fmt.Errorf("channel %s: %w", id, err)
	}
	return c, cfg, nil
}

// GetChannelInfo returns one channel without its configuration, or
// ErrNotFound.
func GetChannelInfo(ctx context.Context, q querier, id string) (Channel, error) {
	c, err := scanChannel(q.QueryRowContext(ctx, `SELECT `+channelColumns+` FROM notification_channels WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// CreateChannel validates and stores a new channel and returns its id. A
// failed validation is a FieldErrors error and nothing is written.
func CreateChannel(ctx context.Context, d *db.DB, key *vault.Key, in ChannelInput, now time.Time) (string, error) {
	if errs := in.validate(); errs != nil {
		return "", errs
	}
	plain, err := notify.Marshal(in.Config)
	if err != nil {
		return "", err
	}
	id := ids.New()
	enc := key.Seal(channelContext(id), plain)
	err = db.Retry(ctx, func() error {
		_, err := d.Writer.ExecContext(ctx, `INSERT INTO notification_channels (id, name, type, enabled, config_enc, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, strings.TrimSpace(in.Name), in.Config.Type(), b2i(in.Enabled), enc, formatTime(now), formatTime(now))
		return err
	})
	return id, err
}

// UpdateChannel replaces a channel's name, enabled flag and configuration,
// or returns ErrNotFound. A secret left blank keeps the stored value, and
// the type of a channel never changes: a configuration of another type is
// ErrNotFound as well.
func UpdateChannel(ctx context.Context, d *db.DB, key *vault.Key, id string, in ChannelInput, now time.Time) error {
	return db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		_, old, err := GetChannel(ctx, tx, key, id)
		if err != nil {
			return err
		}
		if old.Type() != in.Config.Type() {
			return ErrNotFound
		}
		in.Config = in.Config.WithSecretsFrom(old)
		if errs := in.validate(); errs != nil {
			return errs
		}
		plain, err := notify.Marshal(in.Config)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE notification_channels SET name = ?, enabled = ?, config_enc = ?, updated_at = ? WHERE id = ?`,
			strings.TrimSpace(in.Name), b2i(in.Enabled), key.Seal(channelContext(id), plain), formatTime(now), id); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// DeleteChannel removes a channel together with its routes and delivery
// history, or returns ErrNotFound.
func DeleteChannel(ctx context.Context, d *db.DB, id string) error {
	return db.Retry(ctx, func() error {
		res, err := d.Writer.ExecContext(ctx, `DELETE FROM notification_channels WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
