package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/ids"
	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/statuspage"
)

// StatusPage is a public status page without its contents (docs/08
// "Status page"). The password and the unlisted token are never read
// back: only whether they are set.
type StatusPage struct {
	ID            string
	Slug          string
	Title         string
	Description   string
	Visibility    string
	Theme         string
	Accent        string // #rrggbb, or "" for the theme's own
	LogoPath      string // file name under the uploads directory, or ""
	HasToken      bool
	HasPassword   bool
	IncidentDays  int
	ShowPoweredBy bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// StatusPageGroup is a named, ordered group of monitors on a page.
type StatusPageGroup struct {
	ID   string
	Name string
}

// StatusPageMonitor is a monitor shown on a page under a public name.
type StatusPageMonitor struct {
	MonitorID   string
	GroupID     string // "" when ungrouped
	DisplayName string
	ShowLatency bool
	Sort        int
}

// StatusPageDetail is a page with its groups (in order), monitors (in
// order) and mapped hostnames.
type StatusPageDetail struct {
	StatusPage
	Groups   []StatusPageGroup
	Monitors []StatusPageMonitor
	Hosts    []string
}

// StatusPageSummary is a row of the list of pages.
type StatusPageSummary struct {
	StatusPage
	Monitors int
	Hosts    []string
}

// StatusPageInput is what an admin form saves. Groups are names in
// display order; a monitor refers to one by name. Hosts are already
// normalized. A page that is not password-protected loses its password
// hash, and one that is not unlisted loses its token hash.
type StatusPageInput struct {
	Slug, Title, Description, Visibility, Theme, Accent string
	IncidentDays                                        int
	ShowPoweredBy                                       bool
	PasswordHash                                        string // set when not empty; otherwise the stored one is kept
	TokenHash                                           []byte // set when not nil; otherwise the stored one is kept
	Groups                                              []string
	Monitors                                            []StatusPageMonitorInput
	Hosts                                               []string
}

// StatusPageMonitorInput is a monitor to show; Group is a name from
// StatusPageInput.Groups or "".
type StatusPageMonitorInput struct {
	MonitorID, Group, DisplayName string
	ShowLatency                   bool
	Sort                          int
}

const statusPageColumns = `id, slug, title, description, visibility, unlisted_token_hash IS NOT NULL,
	password_hash IS NOT NULL, theme, accent, logo_path, incident_days, show_powered_by, created_at, updated_at`

func scanStatusPage(r scanner) (StatusPage, error) {
	var p StatusPage
	var desc, accent, logo sql.NullString
	var created, updated string
	if err := r.Scan(&p.ID, &p.Slug, &p.Title, &desc, &p.Visibility, &p.HasToken, &p.HasPassword,
		&p.Theme, &accent, &logo, &p.IncidentDays, &p.ShowPoweredBy, &created, &updated); err != nil {
		return p, err
	}
	p.Description, p.Accent, p.LogoPath = desc.String, accent.String, logo.String
	p.CreatedAt, p.UpdatedAt = parseTime(created), parseTime(updated)
	return p, nil
}

// ListStatusPages returns every page by title, with its monitor count and
// hostnames.
func ListStatusPages(ctx context.Context, q querier) ([]StatusPageSummary, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+statusPageColumns+`,
		(SELECT COUNT(*) FROM status_page_monitors m WHERE m.status_page_id = p.id)
		FROM status_pages p ORDER BY title COLLATE NOCASE, slug`)
	if err != nil {
		return nil, err
	}
	var out []StatusPageSummary
	for rows.Next() {
		var s StatusPageSummary
		var desc, accent, logo sql.NullString
		var created, updated string
		if err := rows.Scan(&s.ID, &s.Slug, &s.Title, &desc, &s.Visibility, &s.HasToken, &s.HasPassword,
			&s.Theme, &accent, &logo, &s.IncidentDays, &s.ShowPoweredBy, &created, &updated, &s.Monitors); err != nil {
			rows.Close()
			return nil, err
		}
		s.Description, s.Accent, s.LogoPath = desc.String, accent.String, logo.String
		s.CreatedAt, s.UpdatedAt = parseTime(created), parseTime(updated)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	hosts, err := q.QueryContext(ctx, `SELECT status_page_id, hostname FROM status_page_hosts ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer hosts.Close()
	byPage := map[string][]string{}
	for hosts.Next() {
		var id, host string
		if err := hosts.Scan(&id, &host); err != nil {
			return nil, err
		}
		byPage[id] = append(byPage[id], host)
	}
	for i := range out {
		out[i].Hosts = byPage[out[i].ID]
	}
	return out, hosts.Err()
}

// GetStatusPage returns a page with its contents, or ErrNotFound.
func GetStatusPage(ctx context.Context, q querier, id string) (StatusPageDetail, error) {
	return getStatusPage(ctx, q, `id = ?`, id)
}

// GetStatusPageBySlug returns the page at /status/{slug}, or ErrNotFound.
func GetStatusPageBySlug(ctx context.Context, q querier, slug string) (StatusPageDetail, error) {
	return getStatusPage(ctx, q, `slug = ?`, slug)
}

// GetStatusPageByTokenHash returns the unlisted page whose token hashes to
// hash, or ErrNotFound. A page that is no longer unlisted has no token.
func GetStatusPageByTokenHash(ctx context.Context, q querier, hash []byte) (StatusPageDetail, error) {
	return getStatusPage(ctx, q, `unlisted_token_hash = ? AND visibility = 'unlisted'`, hash)
}

// StatusPageIDByHost returns the id of the page a normalized hostname is
// mapped to, or ErrNotFound.
func StatusPageIDByHost(ctx context.Context, q querier, host string) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT status_page_id FROM status_page_hosts WHERE hostname = ?`, host).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func getStatusPage(ctx context.Context, q querier, where string, arg any) (StatusPageDetail, error) {
	var d StatusPageDetail
	p, err := scanStatusPage(q.QueryRowContext(ctx, `SELECT `+statusPageColumns+` FROM status_pages p WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.StatusPage = p
	id := p.ID

	groups, err := q.QueryContext(ctx, `SELECT id, name FROM status_page_groups WHERE status_page_id = ? ORDER BY sort_order, name`, id)
	if err != nil {
		return d, err
	}
	for groups.Next() {
		var g StatusPageGroup
		if err := groups.Scan(&g.ID, &g.Name); err != nil {
			groups.Close()
			return d, err
		}
		d.Groups = append(d.Groups, g)
	}
	groups.Close()
	if err := groups.Err(); err != nil {
		return d, err
	}

	mons, err := q.QueryContext(ctx, `SELECT monitor_id, group_id, display_name, show_latency, sort_order
		FROM status_page_monitors WHERE status_page_id = ? ORDER BY sort_order, display_name COLLATE NOCASE`, id)
	if err != nil {
		return d, err
	}
	for mons.Next() {
		var m StatusPageMonitor
		var group sql.NullString
		if err := mons.Scan(&m.MonitorID, &group, &m.DisplayName, &m.ShowLatency, &m.Sort); err != nil {
			mons.Close()
			return d, err
		}
		m.GroupID = group.String
		d.Monitors = append(d.Monitors, m)
	}
	mons.Close()
	if err := mons.Err(); err != nil {
		return d, err
	}

	hosts, err := q.QueryContext(ctx, `SELECT hostname FROM status_page_hosts WHERE status_page_id = ? ORDER BY hostname`, id)
	if err != nil {
		return d, err
	}
	defer hosts.Close()
	for hosts.Next() {
		var h string
		if err := hosts.Scan(&h); err != nil {
			return d, err
		}
		d.Hosts = append(d.Hosts, h)
	}
	return d, hosts.Err()
}

// CreateStatusPage stores a new page and returns its id. A failed
// validation is a FieldErrors error.
func CreateStatusPage(ctx context.Context, d *db.DB, in StatusPageInput, now time.Time) (string, error) {
	id := ids.New()
	return id, saveStatusPage(ctx, d, id, true, in, now)
}

// UpdateStatusPage replaces a page's settings, groups, monitors and
// hostnames, or returns ErrNotFound. The logo is not touched.
func UpdateStatusPage(ctx context.Context, d *db.DB, id string, in StatusPageInput, now time.Time) error {
	return saveStatusPage(ctx, d, id, false, in, now)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func saveStatusPage(ctx context.Context, d *db.DB, id string, create bool, in StatusPageInput, now time.Time) error {
	return db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		var hadHash sql.NullString
		var hadToken []byte
		if !create {
			err := tx.QueryRowContext(ctx, `SELECT password_hash, unlisted_token_hash FROM status_pages WHERE id = ?`, id).Scan(&hadHash, &hadToken)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
		}
		errs := FieldErrors{}

		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM status_pages WHERE slug = ? AND id <> ?`, in.Slug, id).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			errs.add("slug", "This address is already used by another page.")
		}
		for _, h := range in.Hosts {
			var other string
			err := tx.QueryRowContext(ctx, `SELECT status_page_id FROM status_page_hosts WHERE hostname = ? AND status_page_id <> ?`, h, id).Scan(&other)
			if err == nil {
				errs.add("hosts", h+" is already mapped to another page.")
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		for _, m := range in.Monitors {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM monitors WHERE id = ?`, m.MonitorID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				errs.add("monitors", "A selected monitor does not exist any more.")
			}
		}

		password := nullable(in.PasswordHash)
		if password == nil && hadHash.Valid {
			password = hadHash.String
		}
		var token any
		if in.TokenHash != nil {
			token = in.TokenHash
		} else if hadToken != nil {
			token = hadToken
		}
		if in.Visibility != statuspage.Password {
			password = nil
		} else if password == nil {
			errs.add("password", "Enter a password for the page.")
		}
		if in.Visibility != statuspage.Unlisted {
			token = nil
		} else if token == nil {
			return errors.New("store: an unlisted status page needs a token hash")
		}
		if len(errs) > 0 {
			return errs
		}

		if create {
			_, err = tx.ExecContext(ctx, `INSERT INTO status_pages (id, slug, title, description, visibility, unlisted_token_hash,
				password_hash, theme, accent, incident_days, show_powered_by, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, in.Slug, in.Title, nullable(in.Description), in.Visibility, token, password, in.Theme,
				nullable(in.Accent), in.IncidentDays, b2i(in.ShowPoweredBy), formatTime(now), formatTime(now))
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE status_pages SET slug = ?, title = ?, description = ?, visibility = ?,
				unlisted_token_hash = ?, password_hash = ?, theme = ?, accent = ?, incident_days = ?, show_powered_by = ?,
				updated_at = ? WHERE id = ?`,
				in.Slug, in.Title, nullable(in.Description), in.Visibility, token, password, in.Theme,
				nullable(in.Accent), in.IncidentDays, b2i(in.ShowPoweredBy), formatTime(now), id)
		}
		if err != nil {
			return err
		}
		if err := writeStatusPageGroups(ctx, tx, id, in); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM status_page_hosts WHERE status_page_id = ?`, id); err != nil {
			return err
		}
		for _, h := range in.Hosts {
			if _, err := tx.ExecContext(ctx, `INSERT INTO status_page_hosts (hostname, status_page_id) VALUES (?, ?)`, h, id); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

// writeStatusPageGroups makes the page's groups and monitor mappings equal
// to the input. A group that keeps its name keeps its id.
func writeStatusPageGroups(ctx context.Context, tx *sql.Tx, id string, in StatusPageInput) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM status_page_groups WHERE status_page_id = ?`, id)
	if err != nil {
		return err
	}
	existing := map[string]string{} // lower-case name -> id
	for rows.Next() {
		var gid, name string
		if err := rows.Scan(&gid, &name); err != nil {
			rows.Close()
			return err
		}
		existing[strings.ToLower(name)] = gid
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	keep := map[string]string{} // lower-case name -> id, in the input
	for i, name := range in.Groups {
		key := strings.ToLower(name)
		gid, ok := existing[key]
		if ok {
			_, err = tx.ExecContext(ctx, `UPDATE status_page_groups SET name = ?, sort_order = ? WHERE id = ?`, name, i, gid)
		} else {
			gid = ids.New()
			_, err = tx.ExecContext(ctx, `INSERT INTO status_page_groups (id, status_page_id, name, sort_order) VALUES (?, ?, ?, ?)`, gid, id, name, i)
		}
		if err != nil {
			return err
		}
		keep[key] = gid
	}
	for key, gid := range existing {
		if _, ok := keep[key]; !ok {
			if _, err := tx.ExecContext(ctx, `DELETE FROM status_page_groups WHERE id = ?`, gid); err != nil {
				return err
			}
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM status_page_monitors WHERE status_page_id = ?`, id); err != nil {
		return err
	}
	for _, m := range in.Monitors {
		var group any
		if m.Group != "" {
			gid, ok := keep[strings.ToLower(m.Group)]
			if !ok {
				return errors.New("store: a status page monitor refers to an unknown group")
			}
			group = gid
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO status_page_monitors (status_page_id, monitor_id, group_id, display_name, show_latency, sort_order)
			VALUES (?, ?, ?, ?, ?, ?)`, id, m.MonitorID, group, m.DisplayName, b2i(m.ShowLatency), m.Sort); err != nil {
			return err
		}
	}
	return nil
}

// SetStatusPageToken replaces the token hash of an unlisted page: the old
// address stops working at once. ErrNotFound when there is no such page or
// it is not unlisted.
func SetStatusPageToken(ctx context.Context, d *db.DB, id string, hash []byte, now time.Time) error {
	return db.Retry(ctx, func() error {
		res, err := d.Writer.ExecContext(ctx, `UPDATE status_pages SET unlisted_token_hash = ?, updated_at = ?
			WHERE id = ? AND visibility = 'unlisted'`, hash, formatTime(now), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// DeleteStatusPage removes a page with its groups, monitors and hostnames,
// or returns ErrNotFound.
func DeleteStatusPage(ctx context.Context, d *db.DB, id string) error {
	return db.Retry(ctx, func() error {
		res, err := d.Writer.ExecContext(ctx, `DELETE FROM status_pages WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// SetStatusPageLogo stores the file name of a page's logo ("" removes it)
// and returns the name it replaces, for the caller to delete. ErrNotFound
// when there is no such page.
func SetStatusPageLogo(ctx context.Context, d *db.DB, id, name string, now time.Time) (previous string, err error) {
	err = db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var old sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT logo_path FROM status_pages WHERE id = ?`, id).Scan(&old); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE status_pages SET logo_path = ?, updated_at = ? WHERE id = ?`, nullable(name), formatTime(now), id); err != nil {
			return err
		}
		previous = old.String
		return tx.Commit()
	})
	return previous, err
}

// PageIncident is an incident as a status page shows it: under the
// public name of its monitor, without its summary or failure kind.
type PageIncident struct {
	ID          string
	MonitorID   string
	DisplayName string
	StartedAt   time.Time
	EndedAt     *time.Time // nil while it is active
	Notes       []PageNote // published notes only, oldest first
}

// PageNote is a published manual note.
type PageNote struct {
	Message string
	At      time.Time
}

// ListPageIncidents returns, newest first, up to limit incidents of the
// monitors on a page that are active or ended at or after since, each
// with its published notes (docs/12 "Incident history").
func ListPageIncidents(ctx context.Context, q querier, pageID string, since time.Time, limit int) ([]PageIncident, error) {
	rows, err := q.QueryContext(ctx, `SELECT i.id, i.monitor_id, spm.display_name, i.started_at, i.ended_at
		FROM incidents i JOIN status_page_monitors spm ON spm.monitor_id = i.monitor_id
		WHERE spm.status_page_id = ? AND (i.ended_at IS NULL OR i.ended_at >= ?)
		ORDER BY i.started_at DESC LIMIT ?`, pageID, formatTime(since), limit)
	if err != nil {
		return nil, err
	}
	var out []PageIncident
	byID := map[string]int{}
	for rows.Next() {
		var in PageIncident
		var started string
		var ended sql.NullString
		if err := rows.Scan(&in.ID, &in.MonitorID, &in.DisplayName, &started, &ended); err != nil {
			rows.Close()
			return nil, err
		}
		in.StartedAt, in.EndedAt = parseTime(started), parseNullTime(ended)
		byID[in.ID] = len(out)
		out = append(out, in)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}

	notes, err := q.QueryContext(ctx, `SELECT e.incident_id, COALESCE(e.message, ''), e.created_at
		FROM incident_events e JOIN incidents i ON i.id = e.incident_id
		JOIN status_page_monitors spm ON spm.monitor_id = i.monitor_id
		WHERE spm.status_page_id = ? AND e.event_type = ? AND e.published = 1
			AND (i.ended_at IS NULL OR i.ended_at >= ?)
		ORDER BY e.id`, pageID, incident.EventManualNote, formatTime(since))
	if err != nil {
		return nil, err
	}
	defer notes.Close()
	for notes.Next() {
		var id, at string
		var n PageNote
		if err := notes.Scan(&id, &n.Message, &at); err != nil {
			return nil, err
		}
		if i, ok := byID[id]; ok {
			n.At = parseTime(at)
			out[i].Notes = append(out[i].Notes, n)
		}
	}
	return out, notes.Err()
}
