package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/audit"
	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/statuspage"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// statusPageFormMaxBody bounds a posted status page form: five fields for
// each of a thousand monitors fits well inside.
const statusPageFormMaxBody = 2 << 20

// StatusPages serves the status page admin (docs/03, docs/12). Everything
// on it is for admins: it names hostnames, internal monitor names and the
// way into password and unlisted pages.
type StatusPages struct {
	db       *db.DB
	baseHost string // host of the instance's own base URL, "" when unset
	log      *slog.Logger
	now      func() time.Time
}

// NewStatusPages returns the status page admin handler. baseURL is
// SINJAL_BASE_URL: its host may not be mapped to a page.
func NewStatusPages(d *db.DB, baseURL string, logger *slog.Logger) *StatusPages {
	var host string
	if u, err := url.Parse(baseURL); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	return &StatusPages{db: d, baseHost: host, log: logging.Sub(logger, "http"), now: time.Now}
}

// RegisterStatusPages mounts the admin pages; they must sit behind
// RequireAdmin. Issuing a new unlisted address also needs a recent
// confirmation of the password (docs/13).
func RegisterStatusPages(r chi.Router, h *StatusPages, recent func(http.Handler) http.Handler) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/status-pages", http.HandlerFunc(h.list))
	}
	r.Get("/status-pages/new", h.newForm)
	r.Post("/status-pages", h.create)
	r.Get("/status-pages/{id}/edit", h.editForm)
	r.Post("/status-pages/{id}", h.update)
	r.With(recent).Post("/status-pages/{id}/token", h.regenerateToken)
	r.Post("/status-pages/{id}/delete", h.remove)
}

func (h *StatusPages) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.log.Error("status page request failed", "route", r.URL.Path, "while", what, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func (h *StatusPages) list(w http.ResponseWriter, r *http.Request) {
	pages, err := store.ListStatusPages(r.Context(), h.db.Reader)
	if err != nil {
		h.fail(w, r, "listing status pages", err)
		return
	}
	var v templates.StatusPageListView
	for _, p := range pages {
		v.Rows = append(v.Rows, templates.StatusPageRow{ID: p.ID, Title: p.Title, Slug: p.Slug, Visibility: p.Visibility,
			Address: templates.PageAddress(p.Slug, p.Visibility), Monitors: p.Monitors, Hosts: p.Hosts})
	}
	render(w, r, h.log, http.StatusOK, templates.StatusPageList(pageFor(r, "Status Pages — Sinjal"), v))
}

// newForm serves GET /status-pages/new: public, the paper theme, 30 days of
// incidents, nothing shown yet.
func (h *StatusPages) newForm(w http.ResponseWriter, r *http.Request) {
	h.renderForm(w, r, http.StatusOK, templates.StatusPageForm{Visibility: statuspage.Public, Theme: statuspage.DefaultTheme,
		IncidentDays: strconv.Itoa(statuspage.DefaultIncidentDays), ShowPoweredBy: true, Errors: map[string]string{}})
}

func (h *StatusPages) editForm(w http.ResponseWriter, r *http.Request) {
	p, err := store.GetStatusPage(r.Context(), h.db.Reader, chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		h.fail(w, r, "loading a status page", err)
	default:
		h.renderForm(w, r, http.StatusOK, formFromPage(p))
	}
}

// formFromPage is the form showing a stored page. Monitor rows are added by
// renderForm.
func formFromPage(p store.StatusPageDetail) templates.StatusPageForm {
	f := templates.StatusPageForm{ID: p.ID, Title: p.Title, Slug: p.Slug, Description: p.Description, Visibility: p.Visibility,
		Theme: p.Theme, Accent: p.Accent, IncidentDays: strconv.Itoa(p.IncidentDays), ShowPoweredBy: p.ShowPoweredBy,
		HasPassword: p.HasPassword, HasToken: p.HasToken, Hosts: strings.Join(p.Hosts, "\n"), Errors: map[string]string{}}
	groupName := map[string]string{}
	var names []string
	for _, g := range p.Groups {
		groupName[g.ID] = g.Name
		names = append(names, g.Name)
	}
	f.Groups = strings.Join(names, "\n")
	for _, m := range p.Monitors {
		f.Monitors = append(f.Monitors, templates.StatusPageMonitorRow{ID: m.MonitorID, Show: true, DisplayName: m.DisplayName,
			Group: groupName[m.GroupID], Order: strconv.Itoa(m.Sort), ShowLatency: m.ShowLatency})
	}
	return f
}

// renderForm adds every monitor of the instance as a row (keeping what the
// form already says about it), the group choices and the page origin, and
// renders the page.
func (h *StatusPages) renderForm(w http.ResponseWriter, r *http.Request, status int, f templates.StatusPageForm) {
	monitors, err := store.ListMonitors(r.Context(), h.db.Reader)
	if err != nil {
		h.fail(w, r, "listing monitors", err)
		return
	}
	f.Monitors = monitorRows(monitors, f.Monitors)
	f.GroupNames = lines(f.Groups)
	f.Origin = requestOrigin(r)
	title := "New status page — Sinjal"
	if f.Editing() {
		title = "Edit " + f.Title + " — Sinjal"
	}
	render(w, r, h.log, status, templates.StatusPageFormPage(pageFor(r, title), f))
}

// monitorRows has one row per monitor: those the form shows first, by
// position then name, the others by name. have are the rows already known.
func monitorRows(monitors []store.Monitor, have []templates.StatusPageMonitorRow) []templates.StatusPageMonitorRow {
	known := map[string]templates.StatusPageMonitorRow{}
	for _, row := range have {
		known[row.ID] = row
	}
	rows := make([]templates.StatusPageMonitorRow, 0, len(monitors))
	for _, m := range monitors {
		row, ok := known[m.ID]
		if !ok {
			row = templates.StatusPageMonitorRow{ID: m.ID}
		}
		row.Name = m.Name
		rows = append(rows, row)
	}
	position := func(r templates.StatusPageMonitorRow) int { n, _ := strconv.Atoi(r.Order); return n }
	slices.SortStableFunc(rows, func(a, b templates.StatusPageMonitorRow) int {
		switch {
		case a.Show != b.Show:
			if a.Show {
				return -1
			}
			return 1
		case a.Show && position(a) != position(b):
			return position(a) - position(b)
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return rows
}

// lines splits a textarea into its non-empty trimmed lines.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// parseForm reads a posted form and checks everything that needs no
// database. The input is only complete when the form has no Errors. The
// password is returned apart, to be hashed once the rest is valid.
func (h *StatusPages) parseForm(v url.Values, monitors []store.Monitor, old *store.StatusPageDetail) (f templates.StatusPageForm, in store.StatusPageInput, password string) {
	f = templates.StatusPageForm{Title: strings.TrimSpace(v.Get("title")), Slug: strings.TrimSpace(v.Get("slug")),
		Description: strings.TrimSpace(v.Get("description")), Visibility: v.Get("visibility"), Theme: v.Get("theme"),
		Accent: strings.TrimSpace(v.Get("accent")), IncidentDays: strings.TrimSpace(v.Get("incident_days")),
		ShowPoweredBy: v.Get("show_powered_by") == "1", Groups: v.Get("groups"), Hosts: v.Get("hosts"), Errors: map[string]string{}}
	if old != nil {
		f.ID, f.HasPassword, f.HasToken = old.ID, old.HasPassword, old.HasToken
	}
	bad := func(k, msg string) { addErr(f.Errors, k, msg) }

	switch {
	case f.Title == "":
		bad("title", "Enter a title.")
	case utf8.RuneCountInString(f.Title) > statuspage.MaxTitleLen:
		bad("title", "Use at most "+strconv.Itoa(statuspage.MaxTitleLen)+" characters.")
	}
	slug, ok := statuspage.NormalizeSlug(f.Slug)
	if !ok {
		bad("slug", "Use 1 to 64 lowercase letters, digits and hyphens, not starting or ending with a hyphen.")
	}
	if utf8.RuneCountInString(f.Description) > statuspage.MaxDescriptionLen {
		bad("description", "Use at most "+strconv.Itoa(statuspage.MaxDescriptionLen)+" characters.")
	}
	if !slices.Contains(statuspage.Visibilities, f.Visibility) {
		bad("visibility", "Choose who can see the page.")
	}
	if !slices.Contains(statuspage.Themes, f.Theme) {
		bad("theme", "Choose a theme.")
	}
	accent, ok := statuspage.NormalizeAccent(f.Accent)
	switch {
	case !ok:
		bad("accent", "Enter a colour as #rrggbb, or leave it empty.")
	case accent != "":
		if msg := statuspage.CheckAccent(accent, f.Theme); msg != "" {
			bad("accent", msg)
		}
	}
	days, err := strconv.Atoi(f.IncidentDays)
	if err != nil || days < statuspage.MinIncidentDays || days > statuspage.MaxIncidentDays {
		bad("incident_days", "Enter a whole number of days from 1 to 365.")
	}
	password = v.Get("password")
	if f.Visibility == statuspage.Password {
		switch n := utf8.RuneCountInString(password); {
		case n == 0 && !f.HasPassword:
			bad("password", "Enter a password for the page.")
		case n > 0 && n < statuspage.MinPasswordLen:
			bad("password", "Use at least "+strconv.Itoa(statuspage.MinPasswordLen)+" characters.")
		case n > statuspage.MaxPasswordLen:
			bad("password", "Use at most "+strconv.Itoa(statuspage.MaxPasswordLen)+" characters.")
		}
	} else {
		password = ""
	}

	groups := lines(f.Groups)
	seen := map[string]bool{}
	for _, g := range groups {
		key := strings.ToLower(g)
		switch {
		case utf8.RuneCountInString(g) > statuspage.MaxNameLen:
			bad("groups", "Use at most "+strconv.Itoa(statuspage.MaxNameLen)+" characters in a group name.")
		case seen[key]:
			bad("groups", "A group name appears twice: "+g+".")
		}
		seen[key] = true
	}
	if len(groups) > statuspage.MaxGroups {
		bad("groups", "Use at most "+strconv.Itoa(statuspage.MaxGroups)+" groups.")
	}

	var hosts []string
	for _, raw := range strings.FieldsFunc(f.Hosts, func(r rune) bool { return r == '\n' || r == ',' || r == ' ' || r == '\t' || r == '\r' }) {
		host, err := statuspage.NormalizeHost(raw, h.baseHost)
		if err != nil {
			bad("hosts", "Hostname: "+err.Error()+".")
			continue
		}
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) > statuspage.MaxHosts {
		bad("hosts", "Use at most "+strconv.Itoa(statuspage.MaxHosts)+" hostnames.")
	}

	var shown []store.StatusPageMonitorInput
	for _, m := range monitors {
		row := templates.StatusPageMonitorRow{ID: m.ID, Name: m.Name, Show: v.Get("m_show_"+m.ID) == "1",
			DisplayName: strings.TrimSpace(v.Get("m_name_" + m.ID)), Group: v.Get("m_group_" + m.ID),
			Order: strings.TrimSpace(v.Get("m_order_" + m.ID)), ShowLatency: v.Get("m_latency_"+m.ID) == "1"}
		f.Monitors = append(f.Monitors, row)
		if !row.Show {
			continue
		}
		switch {
		case row.DisplayName == "":
			bad("m_name_"+m.ID, "Enter the name the public sees.")
		case utf8.RuneCountInString(row.DisplayName) > statuspage.MaxNameLen:
			bad("m_name_"+m.ID, "Use at most "+strconv.Itoa(statuspage.MaxNameLen)+" characters.")
		}
		if row.Group != "" && !seen[strings.ToLower(row.Group)] {
			bad("m_group_"+m.ID, "Choose one of the groups listed above, or none.")
		}
		order := 0
		if row.Order != "" {
			if order, err = strconv.Atoi(row.Order); err != nil || order < 0 || order > 9999 {
				bad("m_order_"+m.ID, "Enter a whole number from 0 to 9999.")
			}
		}
		shown = append(shown, store.StatusPageMonitorInput{MonitorID: m.ID, Group: row.Group, DisplayName: row.DisplayName,
			ShowLatency: row.ShowLatency, Sort: order})
	}

	in = store.StatusPageInput{Slug: slug, Title: f.Title, Description: f.Description, Visibility: f.Visibility, Theme: f.Theme,
		Accent: accent, IncidentDays: days, ShowPoweredBy: f.ShowPoweredBy, Groups: groups, Monitors: shown, Hosts: hosts}
	return f, in, password
}

func (h *StatusPages) create(w http.ResponseWriter, r *http.Request) { h.save(w, r, "") }

func (h *StatusPages) update(w http.ResponseWriter, r *http.Request) {
	h.save(w, r, chi.URLParam(r, "id"))
}

// save validates a posted form, stores the page and audits it. id is "" for
// a new page. A page that becomes unlisted without an address gets one,
// shown once in the response.
func (h *StatusPages) save(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, statusPageFormMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	var old *store.StatusPageDetail
	if id != "" {
		p, err := store.GetStatusPage(ctx, h.db.Reader, id)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			h.fail(w, r, "loading a status page", err)
			return
		}
		old = &p
	}
	monitors, err := store.ListMonitors(ctx, h.db.Reader)
	if err != nil {
		h.fail(w, r, "listing monitors", err)
		return
	}
	f, in, password := h.parseForm(r.PostForm, monitors, old)
	f.ID = id
	if len(f.Errors) > 0 {
		h.renderForm(w, r, http.StatusUnprocessableEntity, f)
		return
	}

	if password != "" {
		if in.PasswordHash, err = auth.HashPassword(password); err != nil {
			h.fail(w, r, "hashing a page password", err)
			return
		}
	}
	var token string
	if in.Visibility == statuspage.Unlisted && (old == nil || !old.HasToken || old.Visibility != statuspage.Unlisted) {
		if token, in.TokenHash, err = statuspage.NewToken(); err != nil {
			h.fail(w, r, "issuing an unlisted address", err)
			return
		}
	}
	if old == nil {
		id, err = store.CreateStatusPage(ctx, h.db, in, h.now())
	} else {
		err = store.UpdateStatusPage(ctx, h.db, id, in, h.now())
	}
	var fe store.FieldErrors
	switch {
	case errors.As(err, &fe):
		for k, msg := range fe {
			addErr(f.Errors, k, msg)
		}
		h.renderForm(w, r, http.StatusUnprocessableEntity, f)
		return
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "saving a status page", err)
		return
	}
	typ := audit.StatusPageCreated
	if old != nil {
		typ = audit.StatusPageUpdated
	}
	h.audit(r, typ, id, in.Title)
	if token != "" {
		h.showToken(w, r, id, in.Title, token, false)
		return
	}
	http.Redirect(w, r, "/status-pages", http.StatusSeeOther)
}

// showToken renders the one-time page with the secret address of an
// unlisted page. Only the hash is stored, so no later page can show it;
// the response is no-store like every admin page, and the token is never
// logged.
func (h *StatusPages) showToken(w http.ResponseWriter, r *http.Request, id, title, token string, regenerated bool) {
	render(w, r, h.log, http.StatusOK, templates.StatusPageToken(pageFor(r, "Page address — Sinjal"), templates.StatusPageTokenView{
		ID: id, Title: title, URL: requestOrigin(r) + "/s/" + token, Regenerated: regenerated}))
}

// regenerateToken serves POST /status-pages/{id}/token: a new address for
// an unlisted page; the old one stops working at once. It sits behind
// RequireRecentAuth.
func (h *StatusPages) regenerateToken(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), chi.URLParam(r, "id")
	p, err := store.GetStatusPage(ctx, h.db.Reader, id)
	if err == nil && p.Visibility != statuspage.Unlisted {
		err = store.ErrNotFound
	}
	var token string
	if err == nil {
		var hash []byte
		if token, hash, err = statuspage.NewToken(); err == nil {
			err = store.SetStatusPageToken(ctx, h.db, id, hash, h.now())
		}
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "regenerating an unlisted address", err)
		return
	}
	cs, _ := SessionFromContext(ctx)
	h.log.Info("status page address regenerated", "user_id", cs.User.ID, "status_page_id", id)
	h.audit(r, audit.StatusPageTokenRegenerated, id, p.Title)
	h.showToken(w, r, id, p.Title, token, true)
}

// remove serves POST /status-pages/{id}/delete: without confirm=1 it only
// asks.
func (h *StatusPages) remove(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), chi.URLParam(r, "id")
	p, err := store.GetStatusPage(ctx, h.db.Reader, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "loading a status page", err)
		return
	}
	if r.PostFormValue("confirm") != "1" {
		render(w, r, h.log, http.StatusOK, templates.StatusPageDeleteConfirm(pageFor(r, "Delete "+p.Title+" — Sinjal"),
			templates.DeleteConfirmView{ID: id, Name: p.Title}))
		return
	}
	switch err := store.DeleteStatusPage(ctx, h.db, id); {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "deleting a status page", err)
		return
	}
	h.audit(r, audit.StatusPageDeleted, id, p.Title)
	http.Redirect(w, r, "/status-pages", http.StatusSeeOther)
}

// audit records a change after it has been committed; a failure is logged
// and the change stands. The metadata is the title only: never an address,
// token or password.
func (h *StatusPages) audit(r *http.Request, typ, id, title string) {
	cs, _ := SessionFromContext(r.Context())
	ev := audit.Event{UserID: cs.User.ID, Type: typ, ObjectType: "status_page", ObjectID: id, Metadata: map[string]string{"name": title}}
	if err := audit.Record(r.Context(), h.db, ev, h.now()); err != nil {
		h.log.Error("audit event not written", "event", typ, "status_page_id", id, "error", err)
	}
}
