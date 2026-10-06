package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/audit"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/maintenance"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/web/sse"
	"github.com/drilonrecica/sinjal/web/templates"
)

// maintenanceFormMaxBody bounds a posted maintenance form.
const maintenanceFormMaxBody = 64 << 10

// datetimeLocal is the value format of <input type="datetime-local">.
const datetimeLocal = "2006-01-02T15:04"

// Maintenance serves the maintenance pages (docs/03, docs/10
// "Maintenance"). Times on them are in the instance time zone, the one
// windows repeat in.
type Maintenance struct {
	db     *db.DB
	events *sse.Hub
	loc    *time.Location
	log    *slog.Logger
	now    func() time.Time
}

// NewMaintenance returns the maintenance handler; loc nil means UTC.
func NewMaintenance(d *db.DB, events *sse.Hub, loc *time.Location, logger *slog.Logger) *Maintenance {
	if loc == nil {
		loc = time.UTC
	}
	return &Maintenance{db: d, events: events, loc: loc, log: logging.Sub(logger, "http"), now: time.Now}
}

// RegisterMaintenance mounts the list and its live fragment inside
// RequireAuth: viewers see the windows too.
func RegisterMaintenance(r chi.Router, h *Maintenance) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/maintenance", http.HandlerFunc(h.list))
		r.Method(method, "/fragments/maintenance", http.HandlerFunc(h.listFragment))
	}
}

// RegisterMaintenanceChanges mounts the forms and actions; they must sit
// behind RequireAdmin.
func RegisterMaintenanceChanges(r chi.Router, h *Maintenance) {
	r.Get("/maintenance/new", h.newForm)
	r.Post("/maintenance", h.create)
	r.Get("/maintenance/{id}/edit", h.editForm)
	r.Post("/maintenance/{id}", h.update)
	r.Post("/maintenance/{id}/delete", h.remove)
}

func (h *Maintenance) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.log.Error("maintenance request failed", "route", r.URL.Path, "while", what, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func (h *Maintenance) list(w http.ResponseWriter, r *http.Request) {
	v, err := h.listView(r)
	if err != nil {
		h.fail(w, r, "listing maintenance", err)
		return
	}
	render(w, r, h.log, http.StatusOK, templates.MaintenanceList(pageFor(r, "Maintenance — Sinjal"), v))
}

func (h *Maintenance) listFragment(w http.ResponseWriter, r *http.Request) {
	v, err := h.listView(r)
	if err != nil {
		h.fail(w, r, "listing maintenance", err)
		return
	}
	render(w, r, h.log, http.StatusOK, templates.MaintenanceSections(v))
}

// listView sorts the windows into in effect, upcoming (by next start) and
// past (most recent first).
func (h *Maintenance) listView(r *http.Request) (templates.MaintenanceListView, error) {
	ctx := r.Context()
	v := templates.MaintenanceListView{Admin: isAdmin(r), Zone: h.loc.String()}
	windows, err := store.ListMaintenance(ctx, h.db.Reader)
	if err != nil {
		return v, err
	}
	monitors, err := store.ListMonitors(ctx, h.db.Reader)
	if err != nil {
		return v, err
	}
	names := make(map[string]string, len(monitors))
	for _, m := range monitors {
		names[m.ID] = m.Name
	}
	now := h.now()
	type sorted struct {
		row templates.MaintenanceRow
		key time.Time
	}
	var upcoming, past []sorted
	for _, mw := range windows {
		row := templates.MaintenanceRow{ID: mw.ID, Name: mw.Name, Schedule: h.schedule(mw),
			Scope: scopeText(mw.Scope, names), Suppress: mw.Suppress, ExcludeUptime: mw.ExcludeUptime}
		next, ok := mw.Next(now, h.loc)
		switch {
		case ok && !next.From.After(now):
			row.When, row.WhenAt = "Until "+h.clock(next.To, now), rfc3339(next.To)
			v.Active = append(v.Active, row)
		case ok:
			row.When, row.WhenAt = "Next "+h.clock(next.From, now), rfc3339(next.From)
			upcoming = append(upcoming, sorted{row, next.From})
		default:
			// Only a one-time window has no next occurrence once it is over
			// (or a weekly one without days, which validation refuses).
			end := mw.Start.Add(mw.Duration)
			row.When, row.WhenAt = "Ended "+h.clock(end, now), rfc3339(end)
			past = append(past, sorted{row, end})
		}
	}
	slices.SortStableFunc(upcoming, func(a, b sorted) int { return a.key.Compare(b.key) })
	slices.SortStableFunc(past, func(a, b sorted) int { return b.key.Compare(a.key) })
	for _, s := range upcoming {
		v.Upcoming = append(v.Upcoming, s.row)
	}
	for _, s := range past {
		v.Past = append(v.Past, s.row)
	}
	return v, nil
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// clock is a moment in the instance time zone: the time alone today, the
// day and time this year, the full date otherwise.
func (h *Maintenance) clock(t, now time.Time) string { return clockText(t, now, h.loc) }

// clockText is the moment t in loc, relative to now.
func clockText(t, now time.Time, loc *time.Location) string {
	t, now = t.In(loc), now.In(loc)
	switch {
	case t.Year() == now.Year() && t.YearDay() == now.YearDay():
		return t.Format("15:04 MST")
	case t.Year() == now.Year():
		return t.Format("Mon 2 Jan, 15:04 MST")
	}
	return t.Format("2 Jan 2006, 15:04 MST")
}

// schedule describes when a window repeats and for how long.
func (h *Maintenance) schedule(mw maintenance.Window) string {
	start, length := mw.Start.In(h.loc), durationText(mw.Duration)
	switch mw.Recurrence {
	case maintenance.Daily:
		return fmt.Sprintf("Daily at %s for %s", start.Format("15:04"), length)
	case maintenance.Weekly:
		var days []string
		for _, d := range templates.WeekdayOrder {
			if mw.Weekdays&(1<<d) != 0 {
				days = append(days, templates.WeekdayNames[d])
			}
		}
		return fmt.Sprintf("%s at %s for %s", strings.Join(days, ", "), start.Format("15:04"), length)
	}
	return fmt.Sprintf("Once, %s for %s", start.Format("2 Jan 2006 15:04"), length)
}

// durationText is "45 min", "2 h", "1 h 30 min" or "3 d 4 h".
func durationText(d time.Duration) string {
	m := int(d.Minutes())
	switch {
	case m < 60:
		return fmt.Sprintf("%d min", m)
	case m < 24*60 && m%60 == 0:
		return fmt.Sprintf("%d h", m/60)
	case m < 24*60:
		return fmt.Sprintf("%d h %d min", m/60, m%60)
	case m%(24*60) < 60:
		return fmt.Sprintf("%d d", m/(24*60))
	}
	return fmt.Sprintf("%d d %d h", m/(24*60), m%(24*60)/60)
}

// scopeText names the monitors and tags a window covers.
func scopeText(s maintenance.Scope, names map[string]string) string {
	if s.All() {
		return "All monitors"
	}
	var parts []string
	for _, id := range s.Monitors {
		if n, ok := names[id]; ok {
			parts = append(parts, n)
		}
	}
	slices.SortFunc(parts, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	for _, t := range s.Tags {
		parts = append(parts, "tag "+t)
	}
	if len(parts) == 0 {
		return "No monitors left"
	}
	return strings.Join(parts, ", ")
}

// newForm serves GET /maintenance/new: one hour from the next full hour,
// once, holding back notifications and excluded from adjusted uptime.
func (h *Maintenance) newForm(w http.ResponseWriter, r *http.Request) {
	start := h.now().In(h.loc).Truncate(time.Hour).Add(time.Hour)
	f := h.formFromWindow(maintenance.Window{Start: start, Duration: time.Hour, Recurrence: maintenance.None,
		Suppress: true, ExcludeUptime: true})
	h.renderForm(w, r, http.StatusOK, f)
}

func (h *Maintenance) editForm(w http.ResponseWriter, r *http.Request) {
	mw, err := store.GetMaintenance(r.Context(), h.db.Reader, chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		h.fail(w, r, "loading a maintenance window", err)
	default:
		h.renderForm(w, r, http.StatusOK, h.formFromWindow(mw))
	}
}

// formFromWindow is the form showing a stored window.
func (h *Maintenance) formFromWindow(mw maintenance.Window) templates.MaintenanceForm {
	f := templates.MaintenanceForm{
		ID: mw.ID, Name: mw.Name, StartsAt: mw.Start.In(h.loc).Format(datetimeLocal),
		DurationHours: strconv.Itoa(int(mw.Duration / time.Hour)), DurationMinutes: strconv.Itoa(int(mw.Duration % time.Hour / time.Minute)),
		Recurrence: string(mw.Recurrence), Suppress: mw.Suppress, ExcludeUptime: mw.ExcludeUptime,
		ScopeAll: mw.Scope.All(), Errors: map[string]string{},
	}
	for d := range 7 {
		f.Weekdays[d] = mw.Weekdays&(1<<d) != 0
	}
	for _, id := range mw.Scope.Monitors {
		f.Monitors = append(f.Monitors, templates.ScopeOption{Value: id, Checked: true})
	}
	for _, t := range mw.Scope.Tags {
		f.Tags = append(f.Tags, templates.ScopeOption{Value: t, Checked: true})
	}
	return f
}

// formFromValues is the form as posted, with the errors that need no
// database, and the window it describes.
func (h *Maintenance) formFromValues(v url.Values) (templates.MaintenanceForm, maintenance.Window) {
	f := templates.MaintenanceForm{
		Name: v.Get("name"), StartsAt: v.Get("starts_at"),
		DurationHours: strings.TrimSpace(v.Get("duration_hours")), DurationMinutes: strings.TrimSpace(v.Get("duration_minutes")),
		Recurrence: v.Get("recurrence"), Suppress: v.Get("suppress") == "1", ExcludeUptime: v.Get("exclude_uptime") == "1",
		ScopeAll: v.Get("scope") != "selected", Errors: map[string]string{},
	}
	mw := maintenance.Window{Name: f.Name, Recurrence: maintenance.Recurrence(f.Recurrence),
		Suppress: f.Suppress, ExcludeUptime: f.ExcludeUptime}
	if t, err := time.Parse(datetimeLocal, strings.TrimSpace(f.StartsAt)); err == nil {
		mw.Start = maintenance.Local(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, h.loc)
	} else {
		f.Errors["starts_at"] = "Enter a date and time."
	}
	hours := wholeNumber(f.DurationHours, "duration", f.Errors)
	minutes := wholeNumber(f.DurationMinutes, "duration", f.Errors)
	if hours < 0 || minutes < 0 || hours > 1000 {
		addErr(f.Errors, "duration", "Enter whole hours and minutes.")
	}
	if _, bad := f.Errors["duration"]; !bad {
		mw.Duration = time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute
	}
	for _, s := range v["weekday"] {
		if d, err := strconv.Atoi(s); err == nil && d >= 0 && d < 7 {
			f.Weekdays[d] = true
			mw.Weekdays |= 1 << d
		}
	}
	for _, id := range v["monitor"] {
		f.Monitors = append(f.Monitors, templates.ScopeOption{Value: id, Checked: true})
	}
	for _, t := range v["tag"] {
		f.Tags = append(f.Tags, templates.ScopeOption{Value: t, Checked: true})
	}
	if !f.ScopeAll {
		mw.Scope = maintenance.Scope{Monitors: v["monitor"], Tags: v["tag"]}
		if mw.Scope.All() {
			f.Errors["scope"] = "Choose at least one monitor or tag, or all monitors."
		}
	}
	return f, mw
}

// renderForm adds every monitor and tag as a choice, keeping what is
// checked, and renders the page.
func (h *Maintenance) renderForm(w http.ResponseWriter, r *http.Request, status int, f templates.MaintenanceForm) {
	ctx := r.Context()
	monitors, err := store.ListMonitors(ctx, h.db.Reader)
	if err != nil {
		h.fail(w, r, "listing monitors", err)
		return
	}
	tags, err := store.TagsByMonitor(ctx, h.db.Reader)
	if err != nil {
		h.fail(w, r, "listing tags", err)
		return
	}
	checked := func(opts []templates.ScopeOption, value string) bool {
		return slices.ContainsFunc(opts, func(o templates.ScopeOption) bool { return strings.EqualFold(o.Value, value) })
	}
	var ms []templates.ScopeOption
	for _, m := range monitors {
		ms = append(ms, templates.ScopeOption{Value: m.ID, Label: m.Name, Checked: checked(f.Monitors, m.ID)})
	}
	seen := map[string]bool{}
	var ts []templates.ScopeOption
	for _, names := range tags {
		for _, t := range names {
			if !seen[strings.ToLower(t)] {
				seen[strings.ToLower(t)] = true
				ts = append(ts, templates.ScopeOption{Value: t, Label: t, Checked: checked(f.Tags, t)})
			}
		}
	}
	slices.SortFunc(ts, func(a, b templates.ScopeOption) int {
		return strings.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label))
	})
	f.Monitors, f.Tags, f.Zone = ms, ts, h.loc.String()
	title := "Schedule maintenance — Sinjal"
	if f.Editing() {
		title = "Edit " + f.Name + " — Sinjal"
	}
	render(w, r, h.log, status, templates.MaintenanceFormPage(pageFor(r, title), f))
}

func (h *Maintenance) create(w http.ResponseWriter, r *http.Request) { h.save(w, r, "") }

func (h *Maintenance) update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := store.GetMaintenance(r.Context(), h.db.Reader, id); errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		h.fail(w, r, "loading a maintenance window", err)
		return
	}
	h.save(w, r, id)
}

// save validates a posted form, stores the window, announces and audits
// it. id is "" for a new window.
func (h *Maintenance) save(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, maintenanceFormMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	f, mw := h.formFromValues(r.PostForm)
	f.ID, mw.ID = id, id
	var err error
	if len(f.Errors) > 0 {
		// The store's findings on the other fields, so all show at once.
		err = store.CheckMaintenance(ctx, h.db.Reader, mw)
	} else if id == "" {
		id, err = store.CreateMaintenance(ctx, h.db, mw, h.now())
	} else {
		err = store.UpdateMaintenance(ctx, h.db, mw, h.now())
	}
	var fe store.FieldErrors
	switch {
	case errors.As(err, &fe) || len(f.Errors) > 0:
		for k, msg := range fe {
			if _, ok := f.Errors[k]; !ok {
				f.Errors[k] = msg
			}
		}
		h.renderForm(w, r, http.StatusUnprocessableEntity, f)
		return
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "saving a maintenance window", err)
		return
	}
	typ := audit.MaintenanceCreated
	if f.Editing() {
		typ = audit.MaintenanceUpdated
	}
	h.audit(r, typ, id, strings.TrimSpace(mw.Name))
	h.events.PublishMaintenance(id)
	http.Redirect(w, r, "/maintenance", http.StatusSeeOther)
}

// remove serves POST /maintenance/{id}/delete: without confirm=1 it only
// asks.
func (h *Maintenance) remove(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), chi.URLParam(r, "id")
	mw, err := store.GetMaintenance(ctx, h.db.Reader, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "loading a maintenance window", err)
		return
	}
	if r.PostFormValue("confirm") != "1" {
		render(w, r, h.log, http.StatusOK, templates.MaintenanceDeleteConfirm(pageFor(r, "Delete "+mw.Name+" — Sinjal"),
			templates.DeleteConfirmView{ID: id, Name: mw.Name}))
		return
	}
	switch err := store.DeleteMaintenance(ctx, h.db, id); {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "deleting a maintenance window", err)
		return
	}
	h.audit(r, audit.MaintenanceDeleted, id, mw.Name)
	h.events.PublishMaintenance(id)
	http.Redirect(w, r, "/maintenance", http.StatusSeeOther)
}

// audit records a change after it has been committed; a failure is logged
// and the change stands.
func (h *Maintenance) audit(r *http.Request, typ, id, name string) {
	cs, _ := SessionFromContext(r.Context())
	ev := audit.Event{UserID: cs.User.ID, Type: typ, ObjectType: "maintenance", ObjectID: id, Metadata: map[string]string{"name": name}}
	if err := audit.Record(r.Context(), h.db, ev, h.now()); err != nil {
		h.log.Error("audit event not written", "event", typ, "maintenance_id", id, "error", err)
	}
}
