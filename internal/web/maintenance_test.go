package web

import (
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/maintenance"
	"github.com/drilonrecica/sinjal/internal/store"
)

// windowForm is a valid form for a one-time window of one hour starting
// at start (UTC, the test instance's time zone).
func windowForm(name string, start time.Time) url.Values {
	return url.Values{
		"name": {name}, "starts_at": {start.UTC().Format(datetimeLocal)},
		"duration_hours": {"1"}, "duration_minutes": {"0"}, "recurrence": {"none"},
		"suppress": {"1"}, "exclude_uptime": {"1"}, "scope": {"all"},
	}
}

func (e *appEnv) windowID(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := e.db.Reader.QueryRow(`SELECT id FROM maintenance_windows WHERE name = ?`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMaintenanceCreateEditDelete(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	m := e.addMonitor(t, "API", "https://api.example.com", "prod")
	events := e.subscribe(t)

	if rec := e.getAs(t, "a1", "GET", "/maintenance/new"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="starts_at" type="datetime-local"`) {
		t.Fatalf("new form = %d", rec.Code)
	}
	start := time.Now().Add(48 * time.Hour).Truncate(time.Minute)
	f := windowForm("Patch night", start)
	f["recurrence"], f["weekday"] = []string{"weekly"}, []string{"1", "3"}
	f["scope"], f["monitor"], f["tag"] = []string{"selected"}, []string{m}, []string{"PROD"}
	f["duration_hours"], f["duration_minutes"] = []string{"2"}, []string{"30"}
	rec := e.postAs(t, "a1", "/maintenance", f)
	if rec.Code != 303 || rec.Header().Get("Location") != "/maintenance" {
		t.Fatalf("create = %d %s\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	id := e.windowID(t, "Patch night")
	w, err := store.GetMaintenance(t.Context(), e.db.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Start.Equal(start) || w.Duration != 150*time.Minute || w.Recurrence != maintenance.Weekly ||
		w.Weekdays != 1<<1|1<<3 || !w.Suppress || !w.ExcludeUptime ||
		len(w.Scope.Monitors) != 1 || w.Scope.Monitors[0] != m || len(w.Scope.Tags) != 1 || w.Scope.Tags[0] != "prod" {
		t.Fatalf("stored %+v", w)
	}
	waitUntil(t, "the event", func() bool {
		return strings.Contains(events(), "event: maintenance.updated\ndata: {\"maintenance_id\":\""+id+"\"}")
	})

	for _, who := range []string{"a1", "v1"} {
		body := e.getAs(t, who, "GET", "/maintenance").Body.String()
		for _, want := range []string{"Patch night", "Mon, Wed at", "for 2 h 30 min", "API, tag prod", "Notifications held",
			"Excluded from adjusted uptime", "Upcoming", "Times are in UTC.", `sse-connect="/events"`, `hx-trigger="sse:maintenance.updated"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: list lacks %q", who, want)
			}
		}
		admin := strings.Contains(body, "/maintenance/new") || strings.Contains(body, "/edit")
		if admin != (who == "a1") {
			t.Errorf("%s: change links shown = %v", who, admin)
		}
	}

	edit := e.getAs(t, "a1", "GET", "/maintenance/"+id+"/edit").Body.String()
	for _, want := range []string{`value="Patch night"`, `value="` + start.UTC().Format(datetimeLocal) + `"`, `value="2"`, `value="30"`,
		`value="weekly" checked`, `value="1" checked`, `value="3" checked`, `value="` + m + `" checked`, `value="prod" checked`,
		`action="/maintenance/` + id + `/delete"`} {
		if !strings.Contains(edit, want) {
			t.Errorf("edit form lacks %q", want)
		}
	}
	f = windowForm("Patch day", start)
	if rec := e.postAs(t, "a1", "/maintenance/"+id, f); rec.Code != 303 {
		t.Fatalf("update = %d\n%s", rec.Code, rec.Body)
	}
	if w, _ := store.GetMaintenance(t.Context(), e.db.Reader, id); w.Name != "Patch day" || !w.Scope.All() || w.Recurrence != maintenance.None {
		t.Fatalf("after update %+v", w)
	}

	// Delete asks first.
	if rec := e.postAs(t, "a1", "/maintenance/"+id+"/delete", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Delete Patch day?") {
		t.Fatalf("delete without confirm = %d", rec.Code)
	}
	if e.count(t, `SELECT COUNT(*) FROM maintenance_windows`) != 1 {
		t.Fatal("deleted without confirmation")
	}
	if rec := e.postAs(t, "a1", "/maintenance/"+id+"/delete", url.Values{"confirm": {"1"}}); rec.Code != 303 {
		t.Fatalf("delete = %d", rec.Code)
	}
	if e.count(t, `SELECT COUNT(*) FROM maintenance_windows`) != 0 {
		t.Fatal("not deleted")
	}
	for _, path := range []string{"/maintenance/" + id + "/edit"} {
		if rec := e.getAs(t, "a1", "GET", path); rec.Code != 404 {
			t.Errorf("GET %s after delete = %d", path, rec.Code)
		}
	}
	for _, path := range []string{"/maintenance/" + id, "/maintenance/" + id + "/delete"} {
		if rec := e.postAs(t, "a1", path, windowForm("x", start)); rec.Code != 404 {
			t.Errorf("POST %s after delete = %d", path, rec.Code)
		}
	}
	for _, typ := range []string{"maintenance.created", "maintenance.updated", "maintenance.deleted"} {
		if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = ? AND object_type = 'maintenance' AND object_id = ? AND user_id = 'a1'`, typ, id); n != 1 {
			t.Errorf("%d %s audit events", n, typ)
		}
	}
	if n := strings.Count(events(), "event: maintenance.updated"); n != 3 {
		t.Errorf("%d maintenance.updated events, want 3", n)
	}
}

// Every problem is shown at once, keyed to its field, and nothing is
// stored.
func TestMaintenanceFormErrors(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	f := url.Values{"name": {" "}, "starts_at": {"tomorrow"}, "duration_hours": {"x"}, "recurrence": {"weekly"}, "scope": {"selected"}}
	rec := e.postAs(t, "a1", "/maintenance", f)
	body := rec.Body.String()
	if rec.Code != 422 {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{`id="name-error"`, `id="starts_at-error"`, `id="duration-error"`, `id="weekdays-error"`, `id="scope-error"`,
		"Fix 5 problems to save", `href="#weekdays"`} {
		if !strings.Contains(body, want) {
			t.Errorf("response lacks %q", want)
		}
	}
	// The store's rules: a scope naming a monitor that does not exist.
	f = windowForm("x", time.Now().Add(time.Hour))
	f["scope"], f["monitor"] = []string{"selected"}, []string{"gone"}
	f["duration_hours"], f["duration_minutes"] = []string{"0"}, []string{"0"}
	rec = e.postAs(t, "a1", "/maintenance", f)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "A selected monitor does not exist any more.") ||
		!strings.Contains(rec.Body.String(), `id="duration-error"`) {
		t.Fatalf("store validation = %d\n%s", rec.Code, rec.Body)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM maintenance_windows`); n != 0 {
		t.Fatalf("%d windows stored", n)
	}
}

// Windows are grouped by when they apply; with none there is an empty
// state that says what maintenance does.
func TestMaintenanceListSections(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	body := e.getAs(t, "a1", "GET", "/maintenance").Body.String()
	if !strings.Contains(body, "No maintenance scheduled") || !strings.Contains(body, `href="/maintenance/new"`) {
		t.Fatal("no empty state")
	}
	now := time.Now()
	add := func(name string, start time.Time, r maintenance.Recurrence) {
		if _, err := store.CreateMaintenance(t.Context(), e.db, maintenance.Window{Name: name, Start: start, Duration: time.Hour, Recurrence: r}, now); err != nil {
			t.Fatal(err)
		}
	}
	add("running", now.Add(-10*time.Minute), maintenance.None)
	add("later", now.Add(72*time.Hour), maintenance.None)
	add("sooner", now.Add(48*time.Hour), maintenance.None)
	add("over", now.Add(-50*time.Hour), maintenance.None)
	add("older", now.Add(-100*time.Hour), maintenance.None)
	add("nightly", now.Add(-30*24*time.Hour+2*time.Hour), maintenance.Daily)
	body = e.getAs(t, "a1", "GET", "/fragments/maintenance").Body.String()
	order := []string{"In effect now", "running", "Upcoming", "nightly", "sooner", "later", "Past", "over", "older"}
	last := -1
	for _, s := range order {
		i := strings.Index(body, s)
		if i < last {
			t.Fatalf("%q out of order in\n%s", s, body)
		}
		last = i
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "No maintenance scheduled") {
		t.Error("the fragment is a page or still empty")
	}
}

// The posted start is read in the instance time zone.
func TestMaintenanceFormTimeZone(t *testing.T) {
	bel, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	h := NewMaintenance(nil, nil, bel, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f, w := h.formFromValues(url.Values{"starts_at": {"2026-10-25T02:30"}, "duration_hours": {"1"}, "recurrence": {"daily"}})
	if len(f.Errors) != 0 || !w.Start.Equal(time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)) || w.Duration != time.Hour {
		t.Fatalf("errors %v, start %v, duration %v", f.Errors, w.Start, w.Duration)
	}
	if got := h.formFromWindow(w).StartsAt; got != "2026-10-25T02:30" {
		t.Fatalf("shown back as %q", got)
	}
	if got := h.schedule(w); got != "Daily at 02:30 for 1 h" {
		t.Fatalf("schedule = %q", got)
	}
}

func TestDurationText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Minute: "45 min", 2 * time.Hour: "2 h", 90 * time.Minute: "1 h 30 min",
		24 * time.Hour: "1 d", 76 * time.Hour: "3 d 4 h",
	} {
		if got := durationText(d); got != want {
			t.Errorf("%v = %q, want %q", d, got, want)
		}
	}
}
