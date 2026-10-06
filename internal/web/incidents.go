package web

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/audit"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/web/sse"
	"github.com/drilonrecica/sinjal/web/templates"
)

const (
	// incidentListLimit is how many ended incidents the Incidents page
	// shows besides the active ones.
	incidentListLimit = 100
	// incidentNoteMax is the longest note, in characters.
	incidentNoteMax = 1000
	// incidentFormMaxBody bounds a posted note.
	incidentFormMaxBody = 16 << 10
)

// Incidents serves the incident pages (docs/03 "Incidents"): the list, one
// incident's timeline and the admin's notes. Times are in the instance time
// zone.
type Incidents struct {
	db     *db.DB
	events *sse.Hub
	loc    *time.Location
	log    *slog.Logger
	now    func() time.Time
}

// NewIncidents returns the incident handler; loc nil means UTC.
func NewIncidents(d *db.DB, events *sse.Hub, loc *time.Location, logger *slog.Logger) *Incidents {
	if loc == nil {
		loc = time.UTC
	}
	return &Incidents{db: d, events: events, loc: loc, log: logging.Sub(logger, "http"), now: time.Now}
}

// RegisterIncidents mounts the pages and their live fragments inside
// RequireAuth: viewers read incidents too.
func RegisterIncidents(r chi.Router, h *Incidents) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/incidents", http.HandlerFunc(h.list))
		r.Method(method, "/incidents/{id}", http.HandlerFunc(h.detail))
		r.Method(method, "/fragments/incidents", http.HandlerFunc(h.listFragment))
		r.Method(method, "/fragments/incidents/{id}", http.HandlerFunc(h.timelineFragment))
	}
}

// RegisterIncidentChanges mounts the note action; it must sit behind
// RequireAdmin.
func RegisterIncidentChanges(r chi.Router, h *Incidents) {
	r.Post("/incidents/{id}/note", h.note)
}

func (h *Incidents) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.log.Error("incident request failed", "route", r.URL.Path, "while", what, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// list serves GET /incidents.
func (h *Incidents) list(w http.ResponseWriter, r *http.Request) {
	v, err := incidentListView(r.Context(), h.db.Reader, h.loc, h.now(), "")
	if err != nil {
		h.fail(w, r, "listing incidents", err)
		return
	}
	render(w, r, h.log, http.StatusOK, templates.IncidentsPage(pageFor(r, "Incidents — Sinjal"), v))
}

// listFragment serves GET /fragments/incidents; ?monitor= narrows it to one
// monitor, which is how a monitor's Incidents tab refreshes.
func (h *Incidents) listFragment(w http.ResponseWriter, r *http.Request) {
	v, err := incidentListView(r.Context(), h.db.Reader, h.loc, h.now(), r.URL.Query().Get("monitor"))
	if err != nil {
		h.fail(w, r, "listing incidents", err)
		return
	}
	render(w, r, h.log, http.StatusOK, templates.IncidentList(v))
}

// incidentListView builds the list of all incidents, or of one monitor's.
func incidentListView(ctx context.Context, q *sql.DB, loc *time.Location, now time.Time, monitorID string) (templates.IncidentListView, error) {
	v := templates.IncidentListView{Fragment: "/fragments/incidents", ShowMonitor: monitorID == "", Limit: incidentListLimit}
	if monitorID != "" {
		v.Fragment += "?monitor=" + url.QueryEscape(monitorID)
	}
	rows, err := store.ListIncidents(ctx, q, monitorID, incidentListLimit+1)
	if err != nil {
		return v, err
	}
	// One more than the limit was asked for, to know whether there is more.
	ended := 0
	for _, r := range rows {
		if r.EndedAt != nil {
			if ended++; ended > incidentListLimit {
				v.More = true
				continue
			}
		}
		v.Rows = append(v.Rows, incidentRow(r, loc, now))
	}
	return v, nil
}

func incidentRow(r store.IncidentRow, loc *time.Location, now time.Time) templates.IncidentRowView {
	end := now
	if r.EndedAt != nil {
		end = *r.EndedAt
	}
	return templates.IncidentRowView{
		ID: r.ID, MonitorID: r.MonitorID, Monitor: r.MonitorName, Active: r.EndedAt == nil,
		Started: clockText(r.StartedAt, now, loc), StartedAt: rfc3339(r.StartedAt),
		Duration: formatSince(end.Sub(r.StartedAt)), Kind: r.FailureKind, Summary: r.Summary,
		Parent: r.SuppressedByParent, Maintenance: r.MaintenanceOverlap,
	}
}

// eventLabels name the timeline entries; an unknown type shows as it is.
var eventLabels = map[string]string{
	incident.EventDetected:               "First failure",
	incident.EventDeclaredDown:           "Declared down",
	incident.EventRecovered:              "Recovered",
	incident.EventPaused:                 "Monitor paused, incident ended",
	incident.EventNotificationSuppressed: "Notification held back",
	incident.EventNotificationResumed:    "Held notification sent",
	incident.EventManualNote:             "Note",
}

func (h *Incidents) detailView(r *http.Request, id, noteError string) (templates.IncidentDetailView, error) {
	row, events, err := store.GetIncident(r.Context(), h.db.Reader, id)
	if err != nil {
		return templates.IncidentDetailView{}, err
	}
	now := h.now()
	v := templates.IncidentDetailView{Incident: incidentRow(row, h.loc, now), Admin: isAdmin(r), NoteMax: incidentNoteMax, Error: noteError}
	for _, e := range events {
		label, ok := eventLabels[e.Type]
		if !ok {
			label = e.Type
		}
		v.Events = append(v.Events, templates.IncidentEventView{
			Label: label, Message: e.Message, Time: clockText(e.At, now, h.loc), At: rfc3339(e.At),
			Note: e.Type == incident.EventManualNote,
		})
	}
	return v, nil
}

// detail serves GET /incidents/{id}.
func (h *Incidents) detail(w http.ResponseWriter, r *http.Request) {
	v, err := h.detailView(r, chi.URLParam(r, "id"), "")
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		h.fail(w, r, "loading an incident", err)
	default:
		render(w, r, h.log, http.StatusOK, templates.IncidentDetail(pageFor(r, v.Incident.Monitor+" incident — Sinjal"), v))
	}
}

func (h *Incidents) timelineFragment(w http.ResponseWriter, r *http.Request) {
	v, err := h.detailView(r, chi.URLParam(r, "id"), "")
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		h.fail(w, r, "loading an incident", err)
	default:
		render(w, r, h.log, http.StatusOK, templates.IncidentTimeline(v))
	}
}

// note serves POST /incidents/{id}/note: a note on the timeline, which
// others see at once. An empty or too long note is refused with the page
// again.
func (h *Incidents) note(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.Body = http.MaxBytesReader(w, r.Body, incidentFormMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	msg := strings.TrimSpace(r.PostFormValue("note"))
	problem := ""
	switch n := utf8.RuneCountInString(msg); {
	case n == 0:
		problem = "Write a note first."
	case n > incidentNoteMax:
		problem = "The note is too long: at most " + strconv.Itoa(incidentNoteMax) + " characters."
	}
	if problem != "" {
		v, err := h.detailView(r, id, problem)
		switch {
		case errors.Is(err, store.ErrNotFound):
			http.NotFound(w, r)
		case err != nil:
			h.fail(w, r, "loading an incident", err)
		default:
			render(w, r, h.log, http.StatusUnprocessableEntity, templates.IncidentDetail(pageFor(r, v.Incident.Monitor+" incident — Sinjal"), v))
		}
		return
	}
	monitorID, err := store.AddIncidentNote(r.Context(), h.db, id, msg, h.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "adding a note", err)
		return
	}
	cs, _ := SessionFromContext(r.Context())
	ev := audit.Event{UserID: cs.User.ID, Type: audit.IncidentNoted, ObjectType: "incident", ObjectID: id}
	if err := audit.Record(r.Context(), h.db, ev, h.now()); err != nil {
		h.log.Error("audit event not written", "event", audit.IncidentNoted, "incident_id", id, "error", err)
	}
	h.events.PublishIncident(sse.IncidentUpdated, id, monitorID)
	http.Redirect(w, r, "/incidents/"+id, http.StatusSeeOther)
}
