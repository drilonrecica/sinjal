package web

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/store"
)

// addIncident inserts an incident with a detected and a declared event; an
// ended one has a recovered event too.
func (e *appEnv) addIncident(t *testing.T, id, monitor string, start time.Time, end *time.Time, parent bool) {
	t.Helper()
	var ended any
	if end != nil {
		ended = store.FormatTime(*end)
	}
	if _, err := e.db.Writer.Exec(`INSERT INTO incidents (id, monitor_id, started_at, ended_at, initial_failure_kind, summary, suppressed_by_parent, created_at)
		VALUES (?, ?, ?, ?, 'timeout', 'connection refused', ?, ?)`, id, monitor, store.FormatTime(start), ended, parent, store.FormatTime(start)); err != nil {
		t.Fatal(err)
	}
	for _, ev := range [][2]string{{"detected", "timeout after 5s"}, {"declared_down", "connection refused"}} {
		if _, err := e.db.Writer.Exec(`INSERT INTO incident_events (incident_id, event_type, message, created_at) VALUES (?, ?, ?, ?)`,
			id, ev[0], ev[1], store.FormatTime(start)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIncidentsPages(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	api := e.addMonitor(t, "API", "https://api.example.com")
	db := e.addMonitor(t, "Database", "https://db.example.com")

	body := e.getAs(t, "v1", "GET", "/incidents").Body.String()
	if !strings.Contains(body, "No incidents") {
		t.Fatalf("empty state missing:\n%s", body)
	}

	now := time.Now().Truncate(time.Second)
	over := now.Add(-time.Hour)
	e.addIncident(t, "old", api, now.Add(-3*time.Hour), &over, false)
	e.addIncident(t, "live", db, now.Add(-10*time.Minute), nil, true)
	if _, err := e.db.Writer.Exec(`INSERT INTO incident_events (incident_id, event_type, message, created_at) VALUES ('old', 'notification_suppressed', 'down: flapping', ?)`, store.FormatTime(now)); err != nil {
		t.Fatal(err)
	}

	for _, who := range []string{"a1", "v1"} {
		body := e.getAs(t, who, "GET", "/incidents").Body.String()
		for _, want := range []string{"Active", "Ended", "Database", "API", "10m so far", "2h", "Parent down: notification held", "Flapping: notifications held",
			`href="/incidents/live"`, `data-live-incidents`, `sse-connect="/events"`, "sse:incident.opened"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: list lacks %q", who, want)
			}
		}
		if strings.Index(body, "Database") > strings.Index(body, "API") {
			t.Errorf("%s: the active incident is not first", who)
		}
	}
	if rec := e.getAs(t, "v1", "HEAD", "/incidents"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d bytes", rec.Code, rec.Body.Len())
	}

	// The fragment is the list alone, for one monitor when asked.
	frag := e.getAs(t, "v1", "GET", "/fragments/incidents?monitor="+api).Body.String()
	if strings.Contains(frag, "<html") || strings.Contains(frag, "connection refused") == false ||
		strings.Contains(frag, `href="/incidents/live"`) || !strings.Contains(frag, `href="/incidents/old"`) {
		t.Errorf("fragment for one monitor:\n%s", frag)
	}
	if strings.Contains(frag, `class="incident-monitor"`) {
		t.Error("a monitor's own list repeats the monitor's name")
	}

	// The monitor's Incidents tab shows its incidents and refreshes them.
	tab := e.getAs(t, "v1", "GET", "/monitors/"+db+"?tab=incidents").Body.String()
	if !strings.Contains(tab, `href="/incidents/live"`) || strings.Contains(tab, `href="/incidents/old"`) ||
		!strings.Contains(tab, "/fragments/incidents?monitor="+db) {
		t.Errorf("incidents tab:\n%s", tab)
	}

	detail := e.getAs(t, "v1", "GET", "/incidents/live").Body.String()
	for _, want := range []string{"Active:", "Database", "First failure", "timeout after 5s", "Declared down", "down for 10m so far"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	if strings.Contains(detail, "Add a note") {
		t.Error("a viewer is offered the note form")
	}
	if !strings.Contains(e.getAs(t, "a1", "GET", "/incidents/live").Body.String(), "Add a note") {
		t.Error("an admin is not offered the note form")
	}
	if rec := e.getAs(t, "v1", "GET", "/incidents/nope"); rec.Code != 404 {
		t.Errorf("unknown incident = %d", rec.Code)
	}
	if rec := e.getAs(t, "v1", "GET", "/fragments/incidents/live"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Declared down") {
		t.Errorf("timeline fragment = %d", rec.Code)
	}
}

func TestIncidentNotes(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	m := e.addMonitor(t, "API", "https://api.example.com")
	e.addIncident(t, "i1", m, time.Now().Add(-time.Hour), nil, false)
	events := e.subscribe(t)

	if rec := e.postAs(t, "v1", "/incidents/i1/note", url.Values{"note": {"x"}}); rec.Code != 403 {
		t.Fatalf("viewer note = %d", rec.Code)
	}
	if rec := e.postAs(t, "a1", "/incidents/nope/note", url.Values{"note": {"x"}}); rec.Code != 404 {
		t.Fatalf("unknown incident = %d", rec.Code)
	}
	for _, bad := range []string{"  ", strings.Repeat("é", incidentNoteMax+1)} {
		rec := e.postAs(t, "a1", "/incidents/i1/note", url.Values{"note": {bad}})
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), `role="alert"`) {
			t.Fatalf("bad note %.10q = %d", bad, rec.Code)
		}
	}
	var n int
	if err := e.db.Reader.QueryRow(`SELECT COUNT(*) FROM incident_events WHERE event_type = 'manual_note'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d notes after refused ones, %v", n, err)
	}

	rec := e.postAs(t, "a1", "/incidents/i1/note", url.Values{"note": {"  <b>rebooted</b> the router\nat 10:00 "}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/incidents/i1" {
		t.Fatalf("note = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	var msg string
	if err := e.db.Reader.QueryRow(`SELECT message FROM incident_events WHERE event_type = 'manual_note'`).Scan(&msg); err != nil || msg != "<b>rebooted</b> the router\nat 10:00" {
		t.Fatalf("stored %q, %v", msg, err)
	}
	body := e.getAs(t, "v1", "GET", "/incidents/i1").Body.String()
	if !strings.Contains(body, "&lt;b&gt;rebooted&lt;/b&gt; the router") || strings.Contains(body, "<b>rebooted") {
		t.Errorf("note not escaped:\n%s", body)
	}
	if err := e.db.Reader.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'incident.noted' AND object_id = 'i1' AND user_id = 'a1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d audit rows, %v", n, err)
	}
	waitUntil(t, "the event", func() bool {
		return strings.Contains(events(), "event: incident.updated\ndata: {\"incident_id\":\"i1\",\"monitor_id\":\""+m+"\"}")
	})
}
