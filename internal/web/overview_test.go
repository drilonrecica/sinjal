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
	problems, more, counts := summarize(ms, now)
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
	if p, more, _ := summarize(many, now); len(p) != maxProblems || more != 3 {
		t.Fatalf("%d problems and %d more", len(p), more)
	}
	if p, _, _ := summarize([]store.Monitor{{ID: "x", Name: "x", Enabled: true, State: "up"}}, now); len(p) != 0 {
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

	now := time.Now().Truncate(time.Second)
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET current_state = 'down', current_state_since = ? WHERE id = ?`, store.FormatTime(now.Add(-90*time.Second)), api); err != nil {
		t.Fatal(err)
	}
	e.addIncident(t, "i1", api, now.Add(-90*time.Second), nil, false)
	for _, who := range []string{"a1", "v1"} {
		body := e.getAs(t, who, "GET", "/").Body.String()
		for _, want := range []string{"Needs attention", `href="/monitors/` + api + `"`, "API is down, for 1m", "<dt>Down</dt>", "Recent incidents",
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
