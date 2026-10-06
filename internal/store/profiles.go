package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/ids"
	"github.com/drilonrecica/sinjal/internal/notify"
)

// Bounds of a profile's outage reminder (docs/11 "Outage reminder").
const (
	MinReminder = time.Minute
	MaxReminder = 7 * 24 * time.Hour
)

// Severities lists the severities in routing order.
var Severities = []string{string(notify.SeverityInfo), string(notify.SeverityWarning), string(notify.SeverityCritical)}

// Profile is a notification profile: which channels each severity goes
// to, quiet hours and the outage reminder (docs/11).
type Profile struct {
	ID             string
	Name           string
	QuietEnabled   bool
	QuietStart     string // HH:MM in the instance time zone; "" when unset
	QuietEnd       string
	CriticalBypass bool
	ReminderAfter  time.Duration // 0: no reminder
	// Routes maps a severity to the ids of its channels, in name order.
	Routes   map[string][]string
	Monitors int // monitors that use it
}

// ProfileInput is a profile to create or update.
type ProfileInput struct {
	Name           string
	QuietEnabled   bool
	QuietStart     string
	QuietEnd       string
	CriticalBypass bool
	ReminderAfter  time.Duration
	Routes         map[string][]string
}

// validate returns the field errors of in; channels are the ids that
// exist.
func (in ProfileInput) validate(channels []string) FieldErrors {
	errs := FieldErrors{}
	name := strings.TrimSpace(in.Name)
	switch {
	case name == "":
		errs.add("name", "Enter a name.")
	case len([]rune(name)) > MaxNameLen:
		errs.add("name", fmt.Sprintf("Use at most %d characters.", MaxNameLen))
	}
	if in.QuietEnabled {
		start, okStart := notify.ParseClock(in.QuietStart)
		end, okEnd := notify.ParseClock(in.QuietEnd)
		if !okStart {
			errs.add("quiet_start", "Enter a time such as 23:00.")
		}
		if !okEnd {
			errs.add("quiet_end", "Enter a time such as 07:00.")
		}
		if okStart && okEnd && start == end {
			errs.add("quiet_end", "Use an end that differs from the start.")
		}
	}
	if in.ReminderAfter != 0 && (in.ReminderAfter < MinReminder || in.ReminderAfter > MaxReminder) {
		errs.add("reminder", "Use whole minutes from 1 to 10080 (7 days), or leave it empty.")
	}
	for severity, ids := range in.Routes {
		if !slices.Contains(Severities, severity) {
			errs.add("routes", "Choose channels for info, warning or critical only.")
		}
		for _, id := range ids {
			if !slices.Contains(channels, id) {
				errs.add("routes", "A chosen channel no longer exists. Choose again.")
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// ListProfiles returns every profile by name, with its routes and the
// number of monitors using it.
func ListProfiles(ctx context.Context, q querier) ([]Profile, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.name, p.quiet_hours_enabled, COALESCE(p.quiet_start, ''), COALESCE(p.quiet_end, ''),
		p.critical_bypass, COALESCE(p.reminder_after_seconds, 0),
		(SELECT COUNT(*) FROM monitors m WHERE m.notification_profile_id = p.id)
		FROM notification_profiles p ORDER BY p.name COLLATE NOCASE, p.id`)
	if err != nil {
		return nil, err
	}
	var out []Profile
	for rows.Next() {
		var p Profile
		var reminder int64
		if err := rows.Scan(&p.ID, &p.Name, &p.QuietEnabled, &p.QuietStart, &p.QuietEnd, &p.CriticalBypass, &reminder, &p.Monitors); err != nil {
			rows.Close()
			return nil, err
		}
		p.ReminderAfter = time.Duration(reminder) * time.Second
		p.Routes = map[string][]string{}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	routes, err := q.QueryContext(ctx, `SELECT r.profile_id, r.severity, r.channel_id FROM notification_routes r
		JOIN notification_channels c ON c.id = r.channel_id ORDER BY c.name COLLATE NOCASE, c.id`)
	if err != nil {
		return nil, err
	}
	defer routes.Close()
	byID := make(map[string]*Profile, len(out))
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	for routes.Next() {
		var profile, severity, channel string
		if err := routes.Scan(&profile, &severity, &channel); err != nil {
			return nil, err
		}
		if p := byID[profile]; p != nil {
			p.Routes[severity] = append(p.Routes[severity], channel)
		}
	}
	return out, routes.Err()
}

// GetProfile returns one profile with its routes, or ErrNotFound.
func GetProfile(ctx context.Context, q querier, id string) (Profile, error) {
	all, err := ListProfiles(ctx, q)
	if err != nil {
		return Profile{}, err
	}
	for _, p := range all {
		if p.ID == id {
			return p, nil
		}
	}
	return Profile{}, ErrNotFound
}

// CreateProfile stores a new profile with its routes and returns its id.
// It returns FieldErrors when the input is not acceptable.
func CreateProfile(ctx context.Context, d *db.DB, in ProfileInput, now time.Time) (string, error) {
	id := ids.New()
	return id, writeProfile(ctx, d, id, in, now, true)
}

// UpdateProfile replaces a profile and its routes; ErrNotFound when it is
// gone.
func UpdateProfile(ctx context.Context, d *db.DB, id string, in ProfileInput, now time.Time) error {
	return writeProfile(ctx, d, id, in, now, false)
}

func writeProfile(ctx context.Context, d *db.DB, id string, in ProfileInput, now time.Time, create bool) error {
	return db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		channels, err := channelIDs(ctx, tx)
		if err != nil {
			return err
		}
		errs := in.validate(channels)
		if errs == nil {
			errs = FieldErrors{}
		}
		name := strings.TrimSpace(in.Name)
		var other string
		switch err := tx.QueryRowContext(ctx, `SELECT id FROM notification_profiles WHERE name = ? AND id <> ?`, name, id).Scan(&other); {
		case err == nil:
			errs.add("name", "Another profile has this name.")
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if len(errs) > 0 {
			return errs
		}
		var start, end, reminder any
		if in.QuietEnabled {
			start, end = in.QuietStart, in.QuietEnd
		}
		if in.ReminderAfter > 0 {
			reminder = int64(in.ReminderAfter / time.Second)
		}
		at := formatTime(now)
		if create {
			_, err = tx.ExecContext(ctx, `INSERT INTO notification_profiles
				(id, name, quiet_hours_enabled, quiet_start, quiet_end, critical_bypass, reminder_after_seconds, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, name, b2i(in.QuietEnabled), start, end, b2i(in.CriticalBypass), reminder, at, at)
		} else {
			var res sql.Result
			res, err = tx.ExecContext(ctx, `UPDATE notification_profiles SET name = ?, quiet_hours_enabled = ?, quiet_start = ?,
				quiet_end = ?, critical_bypass = ?, reminder_after_seconds = ?, updated_at = ? WHERE id = ?`,
				name, b2i(in.QuietEnabled), start, end, b2i(in.CriticalBypass), reminder, at, id)
			if err == nil {
				if n, _ := res.RowsAffected(); n == 0 {
					return ErrNotFound
				}
			}
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM notification_routes WHERE profile_id = ?`, id); err != nil {
			return err
		}
		for severity, channels := range in.Routes {
			for _, c := range channels {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO notification_routes (profile_id, severity, channel_id)
					VALUES (?, ?, ?)`, id, severity, c); err != nil {
					return err
				}
			}
		}
		return tx.Commit()
	})
}

func channelIDs(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM notification_channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DeleteProfile removes a profile and its routes; the monitors that used
// it keep running without one (nothing is sent for them). ErrNotFound when
// it is gone.
func DeleteProfile(ctx context.Context, d *db.DB, id string) error {
	return db.Retry(ctx, func() error {
		res, err := d.Writer.ExecContext(ctx, `DELETE FROM notification_profiles WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
