package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/ids"
)

// Saved view limits (docs/29): a short list of filter sets, not a dashboard.
const (
	MaxViewName  = 40
	MaxViewText  = 80
	MaxViewTags  = 10
	MaxUserViews = 30
)

// ViewStates are the states a filter names; "flapping" is the overlay
// (monitors with flapping_since set).
var ViewStates = []string{"up", "down", "pending", "flapping", "paused"}

// Sorts of the monitor list; "" is name.
const (
	SortName    = "name"
	SortSlowest = "slowest"
)

// ViewFilters is one filter set: what the list column's search and chips
// and a saved view's filters_json carry. Empty means no restriction.
type ViewFilters struct {
	Text        string   `json:"text,omitempty"`   // matched in name, tag, public label and, for admins, host
	States      []string `json:"states,omitempty"` // any of ViewStates
	Types       []string `json:"types,omitempty"`  // any of Types
	Tags        []string `json:"tags,omitempty"`   // any of these tags
	TLSExpiring bool     `json:"tls_expiring,omitempty"`
	Failing     bool     `json:"failing,omitempty"` // a failed check among the recent ones
}

// Empty reports whether the filters restrict nothing.
func (f ViewFilters) Empty() bool {
	return f.Text == "" && len(f.States) == 0 && len(f.Types) == 0 && len(f.Tags) == 0 && !f.TLSExpiring && !f.Failing
}

// Normalize returns the filters validated and in canonical form: text
// trimmed, names lower case (tags keep their case), duplicates dropped,
// lists sorted. A value outside the allowed sets is an InputError.
func (f ViewFilters) Normalize() (ViewFilters, error) {
	out := ViewFilters{Text: strings.TrimSpace(f.Text), TLSExpiring: f.TLSExpiring, Failing: f.Failing}
	if len([]rune(out.Text)) > MaxViewText {
		return out, &InputError{"text", "Search text is too long."}
	}
	var err error
	if out.States, err = oneOf(f.States, ViewStates, "states"); err != nil {
		return out, err
	}
	if out.Types, err = oneOf(f.Types, Types, "types"); err != nil {
		return out, err
	}
	if len(f.Tags) > MaxViewTags {
		return out, &InputError{"tags", "Too many tags."}
	}
	seen := map[string]bool{}
	for _, t := range f.Tags {
		t, err := NormalizeTag(t)
		if err != nil {
			return out, err
		}
		if k := strings.ToLower(t); !seen[k] {
			seen[k] = true
			out.Tags = append(out.Tags, t)
		}
	}
	slices.SortFunc(out.Tags, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out, nil
}

func oneOf(in, allowed []string, field string) ([]string, error) {
	var out []string
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if !slices.Contains(allowed, s) {
			return nil, &InputError{field, "Unknown value."}
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out, nil
}

// ValidSort reports whether s is a sort of the list ("" is name).
func ValidSort(s string) bool { return s == "" || s == SortName || s == SortSlowest }

// SavedView is one user's named filter set.
type SavedView struct {
	ID        string
	UserID    string
	Name      string
	Filters   ViewFilters
	Sort      string // "" or SortName for name
	CreatedAt time.Time
	UpdatedAt time.Time
}

func checkView(name string, f ViewFilters, sort string) (string, ViewFilters, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", f, &InputError{"name", "Give the view a name."}
	case len([]rune(name)) > MaxViewName:
		return "", f, &InputError{"name", "Use at most 40 characters."}
	case strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return "", f, &InputError{"name", "The name cannot contain control characters."}
	case !ValidSort(sort):
		return "", f, &InputError{"sort", "Unknown sort."}
	}
	f, err := f.Normalize()
	return name, f, err
}

// CreateSavedView stores a view for a user. Names are unique per user,
// ignoring case; a user keeps at most MaxUserViews.
func CreateSavedView(ctx context.Context, d *db.DB, userID, name string, f ViewFilters, sort string, at time.Time) (SavedView, error) {
	name, f, err := checkView(name, f, sort)
	if err != nil {
		return SavedView{}, err
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return SavedView{}, err
	}
	v := SavedView{ID: ids.New(), UserID: userID, Name: name, Filters: f, Sort: sort, CreatedAt: at.UTC(), UpdatedAt: at.UTC()}
	err = db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM saved_views WHERE user_id = ?`, userID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxUserViews {
			return &InputError{"name", "Delete a view before saving another."}
		}
		if err := viewNameFree(ctx, tx, userID, name, ""); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO saved_views (id, user_id, name, filters_json, sort, created_at, updated_at)
			VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?)`, v.ID, userID, name, string(raw), sort, formatTime(at), formatTime(at)); err != nil {
			return err
		}
		return tx.Commit()
	})
	return v, err
}

// viewNameFree fails with an InputError when the user has another view
// (not exceptID) of that name, ignoring case.
func viewNameFree(ctx context.Context, x execer, userID, name, exceptID string) error {
	var one int
	err := x.QueryRowContext(ctx, `SELECT 1 FROM saved_views WHERE user_id = ? AND name = ? COLLATE NOCASE AND id <> ?`,
		userID, name, exceptID).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	return &InputError{"name", "You already have a view with this name."}
}

// UpdateSavedView replaces the name, filters and sort of the user's view,
// or returns ErrNotFound (also for another user's view).
func UpdateSavedView(ctx context.Context, d *db.DB, userID, id, name string, f ViewFilters, sort string, at time.Time) error {
	name, f, err := checkView(name, f, sort)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := viewNameFree(ctx, tx, userID, name, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE saved_views SET name = ?, filters_json = ?, sort = NULLIF(?, ''), updated_at = ?
			WHERE id = ? AND user_id = ?`, name, string(raw), sort, formatTime(at), id, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return tx.Commit()
	})
}

// DeleteSavedView removes the user's view, or returns ErrNotFound.
func DeleteSavedView(ctx context.Context, d *db.DB, userID, id string) error {
	return db.Retry(ctx, func() error {
		res, err := d.Writer.ExecContext(ctx, `DELETE FROM saved_views WHERE id = ? AND user_id = ?`, id, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ListSavedViews returns the user's views by name.
func ListSavedViews(ctx context.Context, q *sql.DB, userID string) ([]SavedView, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, filters_json, COALESCE(sort, ''), created_at, updated_at
		FROM saved_views WHERE user_id = ? ORDER BY name COLLATE NOCASE`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedView
	for rows.Next() {
		v := SavedView{UserID: userID}
		var raw, created, updated string
		if err := rows.Scan(&v.ID, &v.Name, &raw, &v.Sort, &created, &updated); err != nil {
			return nil, err
		}
		// A row that no longer parses (edited by hand) shows as an
		// unrestricted view rather than failing the page.
		_ = json.Unmarshal([]byte(raw), &v.Filters)
		v.CreatedAt, v.UpdatedAt = parseTime(created), parseTime(updated)
		out = append(out, v)
	}
	return out, rows.Err()
}
