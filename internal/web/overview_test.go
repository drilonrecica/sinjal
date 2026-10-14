package web

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/store"
)

func TestSummarize(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	since := now.Add(-3 * time.Minute)
	soon, past, far := now.Add(5*24*time.Hour), now.Add(-time.Hour), now.Add(60*24*time.Hour)
	flap := now.Add(-time.Minute)
	ms := []store.Monitor{
		{ID: "a", Name: "A", Enabled: true, State: "up", TLSNotAfter: &far},
		{ID: "b", Name: "B", Enabled: true, State: "down", StateSince: since},
		{ID: "c", Name: "C", Enabled: true, State: "up", FlappingSince: &flap},
		{ID: "d", Name: "D", Enabled: true, State: "up", TLSNotAfter: &soon},
		{ID: "e", Name: "E", Enabled: true, State: "up", TLSNotAfter: &past},
		{ID: "f", Name: "F", Enabled: false, State: "paused", TLSNotAfter: &past}, // paused: not a problem
		{ID: "g", Name: "G", Enabled: true, State: "pending"},
	}
	problems, more, counts := summarize(ms, nil, "", true, now)
	var got []string
	for _, p := range problems {
		got = append(got, p.State+" "+p.Href+" "+p.Text)
	}
	want := []string{
		"down /monitors/b B is down, for 3m",
		"flapping /monitors/c C is flapping",
		"tls /monitors/d The certificate of D expires in 5 days",
		"tls /monitors/e The certificate of E has expired",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") || more != 0 {
		t.Fatalf("problems = %q (+%d), want %q", got, more, want)
	}
	var c []string
	for _, x := range counts {
		c = append(c, fmt.Sprint(x.Label, "=", x.Value))
	}
	if g := strings.Join(c, " "); g != "Monitors=7 Up=3 Down=1 Flapping=1 Pending=1 Paused=1" {
		t.Fatalf("counts = %s", g)
	}

	// Only so many are listed; the rest is a count.
	var many []store.Monitor
	for i := range maxProblems + 3 {
		many = append(many, store.Monitor{ID: fmt.Sprint(i), Name: "m", Enabled: true, State: "down", StateSince: since})
	}
	if p, more, _ := summarize(many, nil, "", true, now); len(p) != maxProblems || more != 3 {
		t.Fatalf("%d problems and %d more", len(p), more)
	}
	if p, _, _ := summarize([]store.Monitor{{ID: "x", Name: "x", Enabled: true, State: "up"}}, nil, "", true, now); len(p) != 0 {
		t.Fatalf("a healthy monitor is a problem: %v", p)
	}
}

func TestOverviewPage(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")

	body := e.getAs(t, "v1", "GET", "/").Body.String()
	if !strings.Contains(body, "No monitors yet") || strings.Contains(body, "Create monitor") {
		t.Fatalf("empty overview for a viewer:\n%s", body)
	}
	if !strings.Contains(e.getAs(t, "a1", "GET", "/").Body.String(), "Create monitor") {
		t.Error("an admin is not offered the first monitor")
	}

	api := e.addMonitor(t, "API", "https://api.example.com")
	e.addMonitor(t, "Docs", "https://docs.example.com")
	body = e.getAs(t, "v1", "GET", "/").Body.String()
	if strings.Contains(body, "Needs attention") || !strings.Contains(body, "No incidents") ||
		!strings.Contains(body, "<dt>Monitors</dt>") || !strings.Contains(body, `href="/monitors"`) {
		t.Fatalf("a calm overview shows a strip or lacks its parts:\n%s", body)
	}
	for _, want := range []string{`<section class="home-hero" data-state="up"`, ">2 monitors are waiting for their first check.</p>",
		"Recent events", "Nothing has happened yet."} {
		if !strings.Contains(body, want) {
			t.Errorf("calm overview lacks %q", want)
		}
	}

	now := time.Now().Truncate(time.Second)
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET current_state = 'down', current_state_since = ? WHERE id = ?`, store.FormatTime(now.Add(-90*time.Second)), api); err != nil {
		t.Fatal(err)
	}
	e.addIncident(t, "i1", api, now.Add(-90*time.Second), nil, false)
	if _, err := e.db.Writer.Exec(`INSERT INTO incident_events (incident_id, event_type, message, created_at) VALUES ('i1', 'declared_down', 'dial tcp 10.0.0.7:443: refused', ?)`,
		store.FormatTime(now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"a1", "v1"} {
		body := e.getAs(t, who, "GET", "/").Body.String()
		if strings.Contains(body, "10.0.0.7") {
			t.Errorf("%s: Recent events shows an event's message", who)
		}
		for _, want := range []string{`<section class="home-hero" data-state="down"`, ">API is down.</p>",
			`<a class="event-monitor" href="/monitors/` + api + `">API</a>`, `<a class="event-label" href="/incidents/i1">Declared down</a>`,
			"Needs attention", `href="/monitors/` + api + `"`, "API is down, for 1m", "<dt>Down</dt>", "Recent incidents",
			`href="/incidents/i1"`, "All incidents", `hx-get="/fragments/overview"`, "every 60s", `sse-connect="/events"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: overview lacks %q", who, want)
			}
		}
	}
	frag := e.getAs(t, "v1", "GET", "/fragments/overview").Body.String()
	if strings.Contains(frag, "<html") || !strings.Contains(frag, "Needs attention") {
		t.Errorf("fragment:\n%s", frag)
	}
	if rec := e.getAs(t, "v1", "HEAD", "/"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD / = %d with %d bytes", rec.Code, rec.Body.Len())
	}

	// The incident fragment honours a limit and ignores a bad one.
	for q, want := range map[string]string{"?limit=1": "limit=1", "?limit=0": `hx-get="/fragments/incidents"`, "?limit=x": `hx-get="/fragments/incidents"`} {
		if body := e.getAs(t, "v1", "GET", "/fragments/incidents"+q).Body.String(); !strings.Contains(body, want) {
			t.Errorf("%s: lacks %q:\n%s", q, want, body)
		}
	}
}

func TestStatusSentence(t *testing.T) {
	flap := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	m := func(id, state string) store.Monitor {
		return store.Monitor{ID: id, Name: strings.ToUpper(id), Enabled: state != "paused", State: state}
	}
	flapping := func(id string) store.Monitor { x := m(id, "up"); x.FlappingSince = &flap; return x }
	child := func(id, parent, state string) store.Monitor { x := m(id, state); x.ParentMonitorID = parent; return x }

	for _, tt := range []struct {
		name        string
		monitors    []store.Monitor
		text, state string
	}{
		{"one up", []store.Monitor{m("a", "up")}, "A is up.", "up"},
		{"all up", []store.Monitor{m("a", "up"), m("b", "up")}, "All 2 monitors are up.", "up"},
		// "All" only when every monitor is up.
		{"paused", []store.Monitor{m("a", "up"), m("b", "up"), m("c", "paused")}, "2 monitors are up; 1 is paused.", "up"},
		{"pending and paused", []store.Monitor{m("a", "up"), m("b", "pending"), m("c", "pending"), m("d", "paused"), m("e", "paused")},
			"1 monitor is up; 2 are waiting for their first check, 2 are paused.", "up"},
		{"all paused", []store.Monitor{m("a", "paused"), m("b", "paused")}, "All 2 monitors are paused.", "up"},
		{"one paused", []store.Monitor{m("a", "paused")}, "A is paused.", "up"},
		{"all pending", []store.Monitor{m("a", "pending"), m("b", "pending")}, "2 monitors are waiting for their first check.", "up"},
		{"one pending", []store.Monitor{m("a", "pending")}, "A is waiting for its first check.", "up"},
		{"up and one pending", []store.Monitor{m("a", "up"), m("b", "up"), m("c", "pending")}, "2 monitors are up; 1 is waiting for its first check.", "up"},
		{"one down", []store.Monitor{m("a", "up"), m("b", "down")}, "B is down.", "down"},
		{"down and more", []store.Monitor{m("a", "down"), m("b", "down"), flapping("c"), m("d", "up")},
			"A is down. 2 more monitors are affected.", "down"},
		{"one more", []store.Monitor{m("a", "down"), m("b", "down")}, "A is down. 1 more monitor is affected.", "down"},
		// The root cause is named: a child of a down parent is not it.
		{"root", []store.Monitor{child("app", "z", "down"), m("z", "down")}, "Z is down. 1 more monitor is affected.", "down"},
		{"one flapping", []store.Monitor{m("a", "up"), flapping("b")}, "B is flapping.", "flapping"},
		{"flapping", []store.Monitor{flapping("a"), flapping("b")}, "2 monitors are flapping.", "flapping"},
	} {
		text, state := statusSentence(tt.monitors)
		if text != tt.text || state != tt.state {
			t.Errorf("%s: %q (%s), want %q (%s)", tt.name, text, state, tt.text, tt.state)
		}
	}
}

// Channels that fail and a system warning join the strip: the warning
// first (nothing else is reliable while results are not saved), channels
// after the monitors' states and before certificates. The cap counts all.
func TestSummarizeChannelsAndSystem(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	soon := now.Add(5 * 24 * time.Hour)
	ms := []store.Monitor{
		{ID: "b", Name: "B", Enabled: true, State: "down", StateSince: now.Add(-time.Minute)},
		{ID: "d", Name: "D", Enabled: true, State: "up", TLSNotAfter: &soon},
	}
	chans := []store.Channel{
		{ID: "c1", Name: "Ops email", Enabled: true, HealthState: "failed"},
		{ID: "c2", Name: "Chat", Enabled: true, HealthState: "warning"},
		{ID: "c3", Name: "Fine", Enabled: true, HealthState: "healthy"},
		{ID: "c4", Name: "Off", Enabled: false, HealthState: "failed"}, // disabled: not a problem
	}
	lines := func(admin bool) []string {
		problems, _, _ := summarize(ms, chans, "the database stayed busy; check results are being held: busy", admin, now)
		var got []string
		for _, p := range problems {
			got = append(got, p.State+" "+p.Href+" "+p.Text)
		}
		return got
	}
	want := []string{
		"system  Check results are not being saved: the database stayed busy; check results are being held: busy",
		"down /monitors/b B is down, for 1m",
		"channel /notifications Notifications to Ops email are failing",
		"channel /notifications A notification to Chat failed recently",
		"tls /monitors/d The certificate of D expires in 5 days",
	}
	if got := lines(true); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("admin:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Viewers get the warning without the database's own words.
	if got := lines(false); got[0] != "system  Check results are not being saved" {
		t.Fatalf("viewer warning: %q", got[0])
	}

	var many []store.Monitor
	for i := range maxProblems {
		many = append(many, store.Monitor{ID: fmt.Sprint(i), Name: "m", Enabled: true, State: "down", StateSince: now})
	}
	if p, more, _ := summarize(many, chans, "w", true, now); len(p) != maxProblems || more != 3 || p[0].State != "system" {
		t.Fatalf("%d problems and %d more, first %q", len(p), more, p[0].State)
	}
}
