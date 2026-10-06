package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/audit"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/notify"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/vault"
	"github.com/drilonrecica/sinjal/web/templates"
)

// channelFormMaxBody bounds a posted channel form.
const channelFormMaxBody = 64 << 10

// Notifications serves the Notifications pages (docs/03, docs/11). Channel
// configuration holds secrets: they are only ever written, never shown back
// (docs/13), and the pages are for admins to change.
type Notifications struct {
	db  *db.DB
	key *vault.Key
	loc *time.Location
	log *slog.Logger
	now func() time.Time
}

// NewNotifications returns the notifications handler; loc nil means UTC.
func NewNotifications(d *db.DB, key *vault.Key, loc *time.Location, logger *slog.Logger) *Notifications {
	if loc == nil {
		loc = time.UTC
	}
	return &Notifications{db: d, key: key, loc: loc, log: logging.Sub(logger, "http"), now: time.Now}
}

// RegisterNotifications mounts the list inside RequireAuth: viewers see the
// channels and their health, never their configuration.
func RegisterNotifications(r chi.Router, h *Notifications) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/notifications", http.HandlerFunc(h.list))
	}
}

// RegisterNotificationChanges mounts the forms and actions; they must sit
// behind RequireAdmin.
func RegisterNotificationChanges(r chi.Router, h *Notifications) {
	r.Get("/notifications/channels/new", h.newForm)
	r.Post("/notifications/channels", h.create)
	r.Get("/notifications/channels/{id}/edit", h.editForm)
	r.Post("/notifications/channels/{id}", h.update)
	r.Post("/notifications/channels/{id}/delete", h.remove)
}

func (h *Notifications) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.log.Error("notifications request failed", "route", r.URL.Path, "while", what, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func (h *Notifications) list(w http.ResponseWriter, r *http.Request) {
	channels, err := store.ListChannels(r.Context(), h.db.Reader)
	if err != nil {
		h.fail(w, r, "listing channels", err)
		return
	}
	now := h.now()
	v := templates.ChannelListView{Admin: isAdmin(r)}
	for _, c := range channels {
		v.Rows = append(v.Rows, templates.ChannelRow{ID: c.ID, Name: c.Name, TypeLabel: templates.ChannelTypeLabel(c.Type),
			Status: channelStatus(c), Detail: h.channelDetail(c, now)})
	}
	render(w, r, h.log, http.StatusOK, templates.NotificationsPage(pageFor(r, "Notifications — Sinjal"), v))
}

// channelStatus is the health of a channel in words (docs/11).
func channelStatus(c store.Channel) string {
	switch {
	case !c.Enabled:
		return "Disabled"
	case c.HealthState == "healthy":
		return "Healthy"
	case c.HealthState == "warning":
		return "Warning"
	case c.HealthState == "failed":
		return "Failed"
	}
	return "Not used yet"
}

// channelDetail is the last success and failure, in the instance time zone.
func (h *Notifications) channelDetail(c store.Channel, now time.Time) string {
	var parts []string
	if c.LastSuccessAt != nil {
		parts = append(parts, "Last success "+clockText(*c.LastSuccessAt, now, h.loc))
	}
	if c.LastFailureAt != nil {
		f := "Last failure " + clockText(*c.LastFailureAt, now, h.loc)
		if c.LastError != "" {
			f += ": " + c.LastError
		}
		parts = append(parts, f)
	}
	return strings.Join(parts, ". ")
}

// newForm serves GET /notifications/channels/new?type=…; an unknown type
// falls back to the first.
func (h *Notifications) newForm(w http.ResponseWriter, r *http.Request) {
	typ := r.URL.Query().Get("type")
	if templates.ChannelFields(typ) == nil {
		typ = notify.Types[0]
	}
	f := templates.ChannelForm{Type: typ, Enabled: true, Errors: map[string]string{}, SecretSet: map[string]bool{},
		Values: map[string]string{"security": notify.SecuritySTARTTLS, "port": "587"}}
	if typ != notify.TypeSMTP {
		f.Values = map[string]string{}
	}
	h.renderForm(w, r, http.StatusOK, f)
}

func (h *Notifications) editForm(w http.ResponseWriter, r *http.Request) {
	c, cfg, err := store.GetChannel(r.Context(), h.db.Reader, h.key, chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		h.fail(w, r, "loading a channel", err)
	default:
		h.renderForm(w, r, http.StatusOK, templates.ChannelForm{ID: c.ID, Type: c.Type, Name: c.Name, Enabled: c.Enabled,
			Values: cfg.Fields(), SecretSet: cfg.SecretsSet(), Errors: map[string]string{}})
	}
}

func (h *Notifications) renderForm(w http.ResponseWriter, r *http.Request, status int, f templates.ChannelForm) {
	title := "Add channel — Sinjal"
	if f.Editing() {
		title = "Edit " + f.Name + " — Sinjal"
	}
	render(w, r, h.log, status, templates.ChannelFormPage(pageFor(r, title), f))
}

func (h *Notifications) create(w http.ResponseWriter, r *http.Request) { h.save(w, r, "") }

func (h *Notifications) update(w http.ResponseWriter, r *http.Request) {
	h.save(w, r, chi.URLParam(r, "id"))
}

// save stores a posted form and audits it. id is "" for a new channel. The
// type of an existing channel is the stored one, whatever is posted.
func (h *Notifications) save(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, channelFormMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	typ := r.PostForm.Get("type")
	var old notify.Config
	if id != "" {
		c, cfg, err := store.GetChannel(ctx, h.db.Reader, h.key, id)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			h.fail(w, r, "loading a channel", err)
			return
		}
		typ, old = c.Type, cfg
	}
	cfg := notify.FromValues(typ, r.PostForm.Get)
	if cfg == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	in := store.ChannelInput{Name: r.PostForm.Get("name"), Enabled: r.PostForm.Get("enabled") == "1", Config: cfg}

	var err error
	if id == "" {
		id, err = store.CreateChannel(ctx, h.db, h.key, in, h.now())
	} else {
		err = store.UpdateChannel(ctx, h.db, h.key, id, in, h.now())
	}
	var fe store.FieldErrors
	switch {
	case errors.As(err, &fe):
		// Secrets are not shown back, so a stored one is still "set"; one
		// typed into a rejected form has to be entered again.
		set := map[string]bool{}
		if old != nil {
			set = old.SecretsSet()
		}
		f := templates.ChannelForm{ID: chi.URLParam(r, "id"), Type: typ, Name: in.Name, Enabled: in.Enabled,
			Values: cfg.Fields(), SecretSet: set, Errors: fe}
		h.renderForm(w, r, http.StatusUnprocessableEntity, f)
		return
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "saving a channel", err)
		return
	}
	typeAudit := audit.ChannelCreated
	if chi.URLParam(r, "id") != "" {
		typeAudit = audit.ChannelUpdated
	}
	h.audit(r, typeAudit, id, strings.TrimSpace(in.Name), typ)
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

// remove serves POST /notifications/channels/{id}/delete: without
// confirm=1 it only asks.
func (h *Notifications) remove(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), chi.URLParam(r, "id")
	c, err := store.GetChannelInfo(ctx, h.db.Reader, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "loading a channel", err)
		return
	}
	if r.PostFormValue("confirm") != "1" {
		render(w, r, h.log, http.StatusOK, templates.ChannelDeleteConfirm(pageFor(r, "Delete "+c.Name+" — Sinjal"),
			templates.DeleteConfirmView{ID: id, Name: c.Name}))
		return
	}
	switch err := store.DeleteChannel(ctx, h.db, id); {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "deleting a channel", err)
		return
	}
	h.audit(r, audit.ChannelDeleted, id, c.Name, c.Type)
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

// audit records a change after it has been committed; a failure is logged
// and the change stands. The metadata never holds configuration.
func (h *Notifications) audit(r *http.Request, typ, id, name, channelType string) {
	cs, _ := SessionFromContext(r.Context())
	ev := audit.Event{UserID: cs.User.ID, Type: typ, ObjectType: "notification_channel", ObjectID: id,
		Metadata: map[string]string{"name": name, "type": channelType}}
	if err := audit.Record(r.Context(), h.db, ev, h.now()); err != nil {
		h.log.Error("audit event not written", "event", typ, "channel_id", id, "error", err)
	}
}
