package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/audit"
	"github.com/drilonrecica/sinjal/internal/notify"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

const (
	// testSendBound bounds a test or simulated send, so its page is
	// written before the server's 30-second write timeout.
	testSendBound = 20 * time.Second
	// maxSimulating is how many simulated messages are sent at once.
	maxSimulating = 4
	// maxErrorRunes bounds an error kept for a test send, as the
	// dispatcher does.
	maxErrorRunes = 300
)

// profileRow is a profile in words for the list.
func (h *Notifications) profileRow(p store.Profile, channelNames map[string]string) templates.ProfileRow {
	row := templates.ProfileRow{ID: p.ID, Name: p.Name, Quiet: "No quiet hours", Reminder: "No reminder"}
	for _, sev := range templates.Severities {
		var names []string
		for _, id := range p.Routes[sev.Value] {
			names = append(names, channelNames[id])
		}
		if len(names) > 0 {
			row.Routes = append(row.Routes, sev.Label+": "+strings.Join(names, ", "))
		}
	}
	if p.QuietEnabled {
		row.Quiet = "Quiet " + p.QuietStart + "–" + p.QuietEnd + " " + h.loc.String()
		if p.CriticalBypass {
			row.Quiet += ", critical bypasses"
		} else {
			row.Quiet += ", critical held too"
		}
	}
	if p.ReminderAfter > 0 {
		row.Reminder = "Reminder after " + notify.FormatDuration(p.ReminderAfter)
	}
	switch p.Monitors {
	case 0:
		row.Monitors = "Used by no monitor"
	case 1:
		row.Monitors = "Used by 1 monitor"
	default:
		row.Monitors = "Used by " + strconv.Itoa(p.Monitors) + " monitors"
	}
	return row
}

// routeChannels are the matrix rows: every channel, disabled ones marked.
func (h *Notifications) routeChannels(ctx context.Context) ([]templates.RouteChannel, error) {
	channels, err := store.ListChannels(ctx, h.db.Reader)
	if err != nil {
		return nil, err
	}
	out := make([]templates.RouteChannel, len(channels))
	for i, c := range channels {
		out[i] = templates.RouteChannel{ID: c.ID, Name: c.Name, TypeLabel: templates.ChannelTypeLabel(c.Type), Enabled: c.Enabled}
	}
	return out, nil
}

func (h *Notifications) renderProfile(w http.ResponseWriter, r *http.Request, status int, f templates.ProfileForm) {
	channels, err := h.routeChannels(r.Context())
	if err != nil {
		h.fail(w, r, "listing channels", err)
		return
	}
	f.Channels, f.Zone = channels, h.loc.String()
	if f.Errors == nil {
		f.Errors = map[string]string{}
	}
	title := "Add profile — Sinjal"
	if f.Editing() {
		title = "Edit " + f.Name + " — Sinjal"
	}
	render(w, r, h.log, status, templates.ProfileFormPage(pageFor(r, title), f))
}

func (h *Notifications) newProfile(w http.ResponseWriter, r *http.Request) {
	h.renderProfile(w, r, http.StatusOK, templates.ProfileForm{CriticalBypass: true, Routes: map[string]bool{}})
}

func (h *Notifications) editProfile(w http.ResponseWriter, r *http.Request) {
	p, err := store.GetProfile(r.Context(), h.db.Reader, chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "loading a profile", err)
		return
	}
	f := templates.ProfileForm{ID: p.ID, Name: p.Name, QuietEnabled: p.QuietEnabled, QuietStart: p.QuietStart, QuietEnd: p.QuietEnd,
		CriticalBypass: p.CriticalBypass, Monitors: p.Monitors, Routes: map[string]bool{}}
	if p.ReminderAfter > 0 {
		f.Reminder = strconv.Itoa(int(p.ReminderAfter / time.Minute))
	}
	for sev, ids := range p.Routes {
		for _, id := range ids {
			f.Routes[templates.RouteKey(sev, id)] = true
		}
	}
	h.renderProfile(w, r, http.StatusOK, f)
}

func (h *Notifications) createProfile(w http.ResponseWriter, r *http.Request) {
	h.saveProfile(w, r, "")
}

func (h *Notifications) updateProfile(w http.ResponseWriter, r *http.Request) {
	h.saveProfile(w, r, chi.URLParam(r, "id"))
}

// saveProfile stores a posted profile form; id is "" for a new one.
func (h *Notifications) saveProfile(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, channelFormMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	v := r.PostForm
	f := templates.ProfileForm{ID: id, Name: v.Get("name"), QuietEnabled: v.Get("quiet_enabled") == "1",
		QuietStart: v.Get("quiet_start"), QuietEnd: v.Get("quiet_end"), CriticalBypass: v.Get("critical_bypass") == "1",
		Reminder: strings.TrimSpace(v.Get("reminder")), Routes: map[string]bool{}}
	in := store.ProfileInput{Name: f.Name, QuietEnabled: f.QuietEnabled, QuietStart: f.QuietStart, QuietEnd: f.QuietEnd,
		CriticalBypass: f.CriticalBypass, Routes: map[string][]string{}}
	if f.Reminder != "" {
		minutes, err := strconv.Atoi(f.Reminder)
		if err != nil || minutes <= 0 {
			minutes = -1 // out of range: the store says what is accepted
		}
		in.ReminderAfter = time.Duration(minutes) * time.Minute
	}
	for _, route := range v["route"] {
		severity, channel, ok := strings.Cut(route, ":")
		if !ok {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		in.Routes[severity] = append(in.Routes[severity], channel)
		f.Routes[route] = true
	}

	ctx := r.Context()
	var err error
	if id == "" {
		id, err = store.CreateProfile(ctx, h.db, in, h.now())
	} else {
		err = store.UpdateProfile(ctx, h.db, id, in, h.now())
	}
	var fe store.FieldErrors
	switch {
	case errors.As(err, &fe):
		f.Errors = fe
		h.renderProfile(w, r, http.StatusUnprocessableEntity, f)
		return
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "saving a profile", err)
		return
	}
	event := audit.ProfileCreated
	if f.ID != "" {
		event = audit.ProfileUpdated
	}
	h.auditProfile(r, event, id, strings.TrimSpace(in.Name))
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

// removeProfile serves POST /notifications/profiles/{id}/delete: without
// confirm=1 it only asks.
func (h *Notifications) removeProfile(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), chi.URLParam(r, "id")
	p, err := store.GetProfile(ctx, h.db.Reader, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "loading a profile", err)
		return
	}
	if r.PostFormValue("confirm") != "1" {
		render(w, r, h.log, http.StatusOK, templates.ProfileDeleteConfirm(pageFor(r, "Delete "+p.Name+" — Sinjal"),
			templates.DeleteConfirmView{ID: id, Name: p.Name}, p.Monitors))
		return
	}
	switch err := store.DeleteProfile(ctx, h.db, id); {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "deleting a profile", err)
		return
	}
	h.auditProfile(r, audit.ProfileDeleted, id, p.Name)
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

// exampleEvent is the incident a test message describes: an example
// monitor, marked as a test in every channel's format (docs/36).
func exampleEvent(kind notify.Kind, now time.Time) notify.Event {
	started := now.Add(-257 * time.Second)
	e := notify.Event{Kind: kind, Test: true, MonitorName: "Example monitor", MonitorType: store.TypeHTTP, IncidentStart: started}
	latency := 74 * time.Millisecond
	switch kind {
	case notify.KindRecovery:
		latency = 51 * time.Millisecond
		e.At, e.Duration = now, now.Sub(started)
	default:
		e.At, e.Reason, e.Attempts = started, "simulated failure", 2
	}
	e.Latency = &latency
	return e
}

// test serves POST /notifications/channels/{id}/test: a [TEST] DOWN sent
// through the channel as saved, also when it is disabled. It is recorded
// like a delivery (event type test), so it moves the channel's health; it
// belongs to no incident and is not retried.
func (h *Notifications) test(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), chi.URLParam(r, "id")
	c, cfg, err := store.GetChannel(ctx, h.db.Reader, h.key, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "loading a channel", err)
		return
	}
	now := h.now()
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), testSendBound)
	sendErr := h.send(sctx, cfg, notify.Render(exampleEvent(notify.KindDown, now), h.loc))
	cancel()

	result := &templates.SendResult{Channel: c.Name, What: "Test notification"}
	a := store.DeliveryAttempt{ChannelID: id, EventType: "test", Attempt: 1, Status: store.DeliverySent, At: h.now(), Final: true}
	outcome := "sent"
	if sendErr != nil {
		result.Error = errorText(sendErr)
		a.Status, a.Error, outcome = store.DeliveryFailed, result.Error, "failed"
	}
	switch err := store.RecordDelivery(context.WithoutCancel(ctx), h.db, a); {
	case errors.Is(err, store.ErrNotFound):
		// Deleted while the test was on its way: there is nothing to show.
		http.NotFound(w, r)
		return
	case err != nil:
		h.log.Error("the test delivery could not be recorded", "channel_id", id, "error", err)
	case h.events != nil:
		h.events.PublishChannel(id)
	}
	cs, _ := SessionFromContext(ctx)
	if err := audit.Record(ctx, h.db, audit.Event{UserID: cs.User.ID, Type: audit.ChannelTested, ObjectType: "notification_channel",
		ObjectID: id, Metadata: map[string]string{"name": c.Name, "type": c.Type, "result": outcome}}, h.now()); err != nil {
		h.log.Error("audit event not written", "event", audit.ChannelTested, "channel_id", id, "error", err)
	}
	h.renderForm(w, r, http.StatusOK, templates.ChannelForm{ID: c.ID, Type: c.Type, Name: c.Name, Enabled: c.Enabled,
		Values: cfg.Fields(), SecretSet: cfg.SecretsSet(), Errors: map[string]string{}, Test: result})
}

// simulate serves POST /notifications/profiles/{id}/simulate: a [TEST]
// DOWN to the profile's enabled critical channels and a [TEST] RECOVERY to
// its enabled info channels, sent now and in parallel. It exercises the
// routes and the channels without falsifying history: no incident, no
// delivery row, no health change (docs/11 "Testing").
func (h *Notifications) simulate(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), chi.URLParam(r, "id")
	p, err := store.GetProfile(ctx, h.db.Reader, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "loading a profile", err)
		return
	}
	channels, err := store.ListChannels(ctx, h.db.Reader)
	if err != nil {
		h.fail(w, r, "listing channels", err)
		return
	}
	enabled := map[string]store.Channel{}
	for _, c := range channels {
		if c.Enabled {
			enabled[c.ID] = c
		}
	}
	type target struct {
		channel store.Channel
		kind    notify.Kind
		what    string
	}
	var targets []target
	for _, t := range []struct {
		severity string
		kind     notify.Kind
		what     string
	}{{"critical", notify.KindDown, "[TEST] DOWN"}, {"info", notify.KindRecovery, "[TEST] RECOVERY"}} {
		for _, cid := range p.Routes[t.severity] {
			if c, ok := enabled[cid]; ok {
				targets = append(targets, target{c, t.kind, t.what})
			}
		}
	}

	now := h.now()
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), testSendBound)
	defer cancel()
	v := templates.SimulationView{ProfileID: p.ID, Name: p.Name, Quiet: h.quietNote(p, now), Results: make([]templates.SendResult, len(targets))}
	slots := make(chan struct{}, maxSimulating)
	var wg sync.WaitGroup
	for i, t := range targets {
		v.Results[i] = templates.SendResult{Channel: t.channel.Name, What: t.what}
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			_, cfg, err := store.GetChannel(sctx, h.db.Reader, h.key, t.channel.ID)
			if err == nil {
				err = h.send(sctx, cfg, notify.Render(exampleEvent(t.kind, now), h.loc))
			} else if !errors.Is(err, store.ErrNotFound) {
				h.log.Error("a channel's configuration could not be read", "channel_id", t.channel.ID, "error", err)
				err = errors.New("the channel's configuration cannot be read; save it again")
			}
			if err != nil {
				v.Results[i].Error = errorText(err)
			}
		}()
	}
	wg.Wait()
	h.auditProfile(r, audit.ProfileSimulated, p.ID, p.Name)
	render(w, r, h.log, http.StatusOK, templates.SimulationPage(pageFor(r, "Simulated incident — Sinjal"), v))
}

// quietNote says what the profile's quiet hours would do with a real
// incident now; "" without quiet hours.
func (h *Notifications) quietNote(p store.Profile, now time.Time) string {
	if !p.QuietEnabled {
		return ""
	}
	window := p.QuietStart + "–" + p.QuietEnd + " " + h.loc.String()
	if !notify.InQuietHours(p.QuietStart, p.QuietEnd, now, h.loc) {
		return "Quiet hours (" + window + ") are not in effect now: a real incident would be announced as above."
	}
	if p.CriticalBypass {
		return "Quiet hours (" + window + ") are in effect now: a real DOWN would be sent, because critical notifications bypass them, but not its recovery."
	}
	return "Quiet hours (" + window + ") are in effect now: neither a real DOWN nor its recovery would be sent."
}

// auditProfile records a profile change after it has been committed.
func (h *Notifications) auditProfile(r *http.Request, typ, id, name string) {
	cs, _ := SessionFromContext(r.Context())
	ev := audit.Event{UserID: cs.User.ID, Type: typ, ObjectType: "notification_profile", ObjectID: id,
		Metadata: map[string]string{"name": name}}
	if err := audit.Record(r.Context(), h.db, ev, h.now()); err != nil {
		h.log.Error("audit event not written", "event", typ, "profile_id", id, "error", err)
	}
}

// errorText is a sender's error as it is shown and stored: one line
// without secrets already, bounded in length here.
func errorText(err error) string {
	if r := []rune(err.Error()); len(r) > maxErrorRunes {
		return string(r[:maxErrorRunes-1]) + "…"
	}
	return err.Error()
}
