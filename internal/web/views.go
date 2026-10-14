package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/recent"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// The monitor list's search, filters and saved views (docs/29). The state
// of the list is the URL's query (docs/29 "URL state"):
//
//	q       search text           state  up|down|pending|flapping|paused (repeatable)
//	type    monitor type (repeatable)   tag    tag name (repeatable)
//	tls     1: certificate expires within tlsExpiringDays
//	failing 1: a failed check among the recent ones
//	sort    name (default) | slowest
//
// Unknown or invalid values are dropped, never an error: a stale link
// still shows the list.

// tlsExpiringDays is how soon a certificate expires for the "TLS expiring"
// filter: the longest default warning threshold (store.DefaultTLSWarningDays).
const tlsExpiringDays = 30

// parseView reads the list state from query parameters (a URL's or a
// form's), dropping what is invalid.
func parseView(v url.Values) (store.ViewFilters, string) {
	f := store.ViewFilters{
		Text:        strings.TrimSpace(v.Get("q")),
		TLSExpiring: v.Get("tls") == "1",
		Failing:     v.Get("failing") == "1",
	}
	if r := []rune(f.Text); len(r) > store.MaxViewText {
		f.Text = string(r[:store.MaxViewText])
	}
	f.States = keepAllowed(v["state"], store.ViewStates)
	f.Types = keepAllowed(v["type"], store.Types)
	for _, t := range v["tag"] {
		if t, err := store.NormalizeTag(t); err == nil && !slices.Contains(f.Tags, t) && len(f.Tags) < store.MaxViewTags {
			f.Tags = append(f.Tags, t)
		}
	}
	sortBy := v.Get("sort")
	if sortBy != store.SortSlowest {
		sortBy = "" // name, the default
	}
	return f, sortBy
}

func keepAllowed(in, allowed []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.ToLower(strings.TrimSpace(s)); slices.Contains(allowed, s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// viewQuery is the URL query for a list state ("" for the default one).
func viewQuery(f store.ViewFilters, sortBy string) string {
	v := url.Values{}
	if f.Text != "" {
		v.Set("q", f.Text)
	}
	v["state"], v["type"], v["tag"] = f.States, f.Types, f.Tags
	if f.TLSExpiring {
		v.Set("tls", "1")
	}
	if f.Failing {
		v.Set("failing", "1")
	}
	if sortBy == store.SortSlowest {
		v.Set("sort", sortBy)
	}
	for k, s := range v {
		if len(s) == 0 {
			delete(v, k)
		}
	}
	return v.Encode()
}

// columnState is what the shell's search box and chips start from: the
// current URL's list state.
func columnState(r *http.Request) (query, text, state string) {
	f, s := parseView(r.URL.Query())
	if len(f.States) == 1 {
		state = f.States[0]
	}
	return viewQuery(f, s), f.Text, state
}

// listRow is a monitor of the column with what filtering and sorting read.
type listRow struct {
	view templates.MonitorView
	slow int32 // the newest check's duration in ms; -1 without one
}

// filterInput is everything matchView reads besides the monitor.
type filterInput struct {
	now     time.Time
	admin   bool
	tags    []string
	labels  []string
	target  string
	checks  []recent.Check // newest first
	textLow string
}

// matchView reports whether a monitor passes every filter. Within one
// filter any value matches; filters combine with AND.
func matchView(f store.ViewFilters, m store.Monitor, in filterInput) bool {
	if len(f.States) > 0 && !slices.Contains(f.States, displayState(m)) {
		return false
	}
	if len(f.Types) > 0 && !slices.Contains(f.Types, m.Type) {
		return false
	}
	if len(f.Tags) > 0 && !slices.ContainsFunc(f.Tags, func(t string) bool {
		return slices.ContainsFunc(in.tags, func(have string) bool { return strings.EqualFold(have, t) })
	}) {
		return false
	}
	if f.TLSExpiring && (m.TLSNotAfter == nil || m.TLSNotAfter.After(in.now.AddDate(0, 0, tlsExpiringDays))) {
		return false
	}
	if f.Failing && !slices.ContainsFunc(in.checks, func(c recent.Check) bool { return !c.OK }) {
		return false
	}
	return in.textLow == "" || searchMatch(in.textLow, m, in.tags, in.labels, in.admin, in.target)
}

// orderRows sorts rows: by name (the order the monitors come in) or the
// slowest newest check first, those without a check last.
func orderRows(rows []listRow, sortBy string) {
	if sortBy == store.SortSlowest {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].slow > rows[j].slow })
	}
}

// --- Panel and saved views ---

// RegisterViews mounts the saved view changes inside RequireAuth: a view
// belongs to the signed-in user, viewers too (docs/29), so every query
// carries the session's user id.
func RegisterViews(r chi.Router, h *Monitors) {
	r.Post("/views", h.viewSave)
	r.Post("/views/{id}", h.viewUpdate)
	r.Post("/views/{id}/delete", h.viewDelete)
}

// panel serves GET /fragments/column-filters: the column's "More" panel,
// type, tag and sort choices, the two flag filters and the user's saved
// views. The current state comes from the query.
func (h *Monitors) panel(w http.ResponseWriter, r *http.Request) {
	f, s := parseView(r.URL.Query())
	h.renderPanel(w, r, f, s, "", "")
}

func (h *Monitors) renderPanel(w http.ResponseWriter, r *http.Request, f store.ViewFilters, sortBy, notice, problem string) {
	cs, _ := SessionFromContext(r.Context())
	tags, err := store.ListTags(r.Context(), h.db.Reader)
	var views []store.SavedView
	if err == nil {
		views, err = store.ListSavedViews(r.Context(), h.db.Reader, cs.User.ID)
	}
	if err != nil {
		h.log.Error("column panel failed", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	d := templates.ColumnPanelData{
		Types: store.Types, Tags: tags, Tag: first(f.Tags), Type: first(f.Types), Slowest: sortBy == store.SortSlowest,
		TLS: f.TLSExpiring, Failing: f.Failing, Notice: notice, Problem: problem,
	}
	for _, v := range views {
		d.Views = append(d.Views, templates.ColumnView{ID: v.ID, Name: v.Name, Query: viewQuery(v.Filters, v.Sort)})
	}
	render(w, r, h.log, http.StatusOK, templates.ColumnPanel(d))
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// formView reads a posted list state: the column's form fields.
func (h *Monitors) formView(w http.ResponseWriter, r *http.Request) (store.ViewFilters, string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, loginMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return store.ViewFilters{}, "", false
	}
	f, s := parseView(r.PostForm)
	return f, s, true
}

// viewResult answers a saved-view change with the refreshed panel: htmx
// swaps it in place; a validation message sits in the panel.
func (h *Monitors) viewResult(w http.ResponseWriter, r *http.Request, f store.ViewFilters, sortBy, notice string, err error) {
	var in *store.InputError
	switch {
	case err == nil:
		h.renderPanel(w, r, f, sortBy, notice, "")
	case errors.As(err, &in):
		h.renderPanel(w, r, f, sortBy, "", in.Message)
	case errors.Is(err, store.ErrNotFound):
		h.renderPanel(w, r, f, sortBy, "", "That view no longer exists.")
	default:
		h.log.Error("saved view change failed", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// viewSave stores the posted list state under the posted name.
func (h *Monitors) viewSave(w http.ResponseWriter, r *http.Request) {
	f, s, ok := h.formView(w, r)
	if !ok {
		return
	}
	cs, _ := SessionFromContext(r.Context())
	_, err := store.CreateSavedView(r.Context(), h.db, cs.User.ID, r.PostForm.Get("view_name"), f, s, h.now())
	h.viewResult(w, r, f, s, "View saved.", err)
}

// viewUpdate replaces a view's filters and sort with the posted list state.
func (h *Monitors) viewUpdate(w http.ResponseWriter, r *http.Request) {
	f, s, ok := h.formView(w, r)
	if !ok {
		return
	}
	cs, _ := SessionFromContext(r.Context())
	id := chi.URLParam(r, "id")
	views, err := store.ListSavedViews(r.Context(), h.db.Reader, cs.User.ID)
	if err == nil {
		i := slices.IndexFunc(views, func(v store.SavedView) bool { return v.ID == id })
		if i < 0 {
			err = store.ErrNotFound
		} else {
			err = store.UpdateSavedView(r.Context(), h.db, cs.User.ID, id, views[i].Name, f, s, h.now())
		}
	}
	h.viewResult(w, r, f, s, "View updated.", err)
}

// viewDelete removes one of the user's views.
func (h *Monitors) viewDelete(w http.ResponseWriter, r *http.Request) {
	f, s, ok := h.formView(w, r)
	if !ok {
		return
	}
	cs, _ := SessionFromContext(r.Context())
	err := store.DeleteSavedView(r.Context(), h.db, cs.User.ID, chi.URLParam(r, "id"))
	h.viewResult(w, r, f, s, "View deleted.", err)
}
