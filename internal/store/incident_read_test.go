package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/incident"
)

// seedIncident opens an incident for a monitor at start and, when end is
// not zero, closes it then.
func seedIncident(t *testing.T, d interface {
	Begin() (*sql.Tx, error)
}, monitor string, start, end time.Time, parent bool) string {
	t.Helper()
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, _, err := OpenIncident(context.Background(), tx, NewIncident{MonitorID: monitor, StartedAt: start,
		DeclaredAt: start.Add(time.Minute), FailureKind: "timeout", Detected: "first", Summary: "last", SuppressedByParent: parent})
	if err != nil {
		t.Fatal(err)
	}
	if !end.IsZero() {
		if _, _, err := CloseIncident(context.Background(), tx, monitor, end, incident.EventRecovered); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

func incidentIDs(rows []IncidentRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// Active incidents come first, then the ended ones, each newest first; a
// monitor filter and the limit apply to the ended ones.
func TestListIncidents(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	a, b := create(t, d, sample("api")), create(t, d, sample("db"))
	old := seedIncident(t, d.Writer, a, now, now.Add(time.Hour), false)
	newer := seedIncident(t, d.Writer, b, now.Add(2*time.Hour), now.Add(3*time.Hour), true)
	active := seedIncident(t, d.Writer, a, now.Add(4*time.Hour), time.Time{}, false)
	// Started before the others but still active: active ones sort among themselves by start.
	activeB := seedIncident(t, d.Writer, b, now.Add(-time.Hour), time.Time{}, false)

	got, err := ListIncidents(ctx, d.Reader, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{active, activeB, newer, old}; !reflect.DeepEqual(incidentIDs(got), want) {
		t.Fatalf("all = %v, want %v", incidentIDs(got), want)
	}
	if got[0].MonitorName != "api" || got[0].EndedAt != nil || got[2].EndedAt == nil || !got[2].SuppressedByParent {
		t.Fatalf("rows = %+v", got)
	}
	if got[0].FailureKind != "timeout" || got[0].Summary != "last" || !got[0].StartedAt.Equal(now.Add(4*time.Hour)) {
		t.Fatalf("first row = %+v", got[0])
	}

	got, _ = ListIncidents(ctx, d.Reader, a, 10)
	if want := []string{active, old}; !reflect.DeepEqual(incidentIDs(got), want) {
		t.Fatalf("monitor a = %v, want %v", incidentIDs(got), want)
	}
	got, _ = ListIncidents(ctx, d.Reader, "", 1)
	if want := []string{active, activeB, newer}; !reflect.DeepEqual(incidentIDs(got), want) {
		t.Fatalf("limit 1 = %v, want %v (the limit is for ended ones)", incidentIDs(got), want)
	}
	if got, err := ListIncidents(ctx, d.Reader, "missing", 10); err != nil || len(got) != 0 {
		t.Fatalf("unknown monitor = %v, %v", got, err)
	}
}

func TestGetIncidentAndNotes(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	m := create(t, d, sample("api"))
	id := seedIncident(t, d.Writer, m, now, now.Add(time.Hour), false)

	monitor, err := AddIncidentNote(ctx, d, id, "rebooted the router", now.Add(2*time.Hour))
	if err != nil || monitor != m {
		t.Fatalf("AddIncidentNote = %q, %v", monitor, err)
	}
	r, events, err := GetIncident(ctx, d.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != id || r.MonitorName != "api" || r.EndedAt == nil {
		t.Fatalf("incident = %+v", r)
	}
	var types []string
	for _, e := range events {
		types = append(types, e.Type)
	}
	if want := []string{incident.EventDetected, incident.EventDeclaredDown, incident.EventRecovered, incident.EventManualNote}; !reflect.DeepEqual(types, want) {
		t.Fatalf("events = %v, want %v", types, want)
	}
	if last := events[3]; last.Message != "rebooted the router" || !last.At.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("note = %+v", last)
	}

	if _, _, err := GetIncident(ctx, d.Reader, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetIncident(missing) = %v", err)
	}
	if _, err := AddIncidentNote(ctx, d, "missing", "x", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AddIncidentNote(missing) = %v", err)
	}
}

func TestIncidentListPlans(t *testing.T) {
	d := testDB(t)
	active := queryPlan(t, d.Reader, `SELECT i.id FROM incidents i JOIN monitors m ON m.id = i.monitor_id
		WHERE i.ended_at IS NULL ORDER BY i.started_at DESC`)
	if !strings.Contains(active, "idx_incidents_active") {
		t.Errorf("active plan: %s", active)
	}
	one := queryPlan(t, d.Reader, `SELECT i.id FROM incidents i JOIN monitors m ON m.id = i.monitor_id
		WHERE i.ended_at IS NOT NULL AND i.monitor_id = ? ORDER BY i.started_at DESC LIMIT ?`, "m", 10)
	if !strings.Contains(one, "idx_incidents_monitor_time") || strings.Contains(one, "TEMP B-TREE") {
		t.Errorf("monitor plan: %s", one)
	}
	events := queryPlan(t, d.Reader, `SELECT event_type FROM incident_events WHERE incident_id = ? ORDER BY id`, "i")
	if !strings.Contains(events, "idx_incident_events_incident") || strings.Contains(events, "TEMP B-TREE") {
		t.Errorf("timeline plan: %s", events)
	}
}
