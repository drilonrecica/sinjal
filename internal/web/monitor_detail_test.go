package web

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/store"
)

// addFailure stores a failed check result for id.
func (e *appEnv) addFailure(t *testing.T, id string, at time.Time, kind, msg, snippet string) {
	t.Helper()
	if _, err := e.db.Writer.Exec(`INSERT INTO check_results (monitor_id, checked_at, duration_ms, success, protocol_status,
		error_kind, error_message, diagnostic_snippet) VALUES (?, ?, 812, 0, '503', ?, ?, ?)`,
		id, store.FormatTime(at), kind, msg, snippet); err != nil {
		t.Fatal(err)
	}
}

func TestMonitorDetail(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	id := e.addMonitor(t, "API", "https://internal.example:8443/health?key=abc", "prod")
	if err := store.SetSecret(context.Background(), e.db, e.key, id, "auth.bearer", []byte(testToken), monitorsNow); err != nil {
		t.Fatal(err)
	}
	e.addFailure(t, id, monitorsNow, "http_status", "unexpected status 503 from internal.example", `<script>alert(1)</script> down`)

	get := func(user, query string) string {
		t.Helper()
		rec := e.getAs(t, user, "GET", "/monitors/"+id+query)
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s = %d", user, query, rec.Code)
		}
		if strings.Contains(rec.Body.String(), testToken) {
			t.Fatalf("%s %s shows the token", user, query)
		}
		return rec.Body.String()
	}

	// Overview, the default tab, for an admin: header, actions, tabs.
	page := get("a1", "")
	for _, want := range []string{
		`id="monitor-header-` + id + `"`, `data-gone-href="/monitors"`, `sse-connect="/events"`,
		`href="/monitors/` + id + `/edit"`, `action="/monitors/` + id + `/pause"`, `action="/monitors/` + id + `/delete"`,
		`href="/monitors/` + id + `" aria-current="page">Overview`, `href="/monitors/` + id + `?tab=diagnostics"`,
		"Checked every", "30s", "https://internal.example:8443",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("admin overview lacks %q", want)
		}
	}

	// Configuration: admins see the request, secrets only by name.
	config := get("a1", "?tab=configuration")
	for _, want := range []string{"https://internal.example:8443/health?key=abc", "Bearer token (stored encrypted)", "200-399", "prod"} {
		if !strings.Contains(config, want) {
			t.Errorf("admin configuration lacks %q", want)
		}
	}
	// Diagnostics: the snippet is escaped text.
	diag := get("a1", "?tab=diagnostics")
	for _, want := range []string{"HTTP status", "503", "812 ms", "unexpected status 503 from internal.example", "&lt;script&gt;alert(1)&lt;/script&gt; down"} {
		if !strings.Contains(diag, want) {
			t.Errorf("admin diagnostics lacks %q", want)
		}
	}
	if strings.Contains(diag, "<script>alert") {
		t.Error("a response snippet is rendered as markup")
	}

	// Viewers: no actions, no address, no messages or snippets.
	for _, tab := range []string{"", "?tab=configuration", "?tab=diagnostics", "?tab=history", "?tab=incidents"} {
		html := get("v1", tab)
		for _, leak := range []string{"internal.example", "key=abc", "Bearer", "alert(1)", "/pause", "/edit", "/delete"} {
			if strings.Contains(html, leak) {
				t.Errorf("viewer %q sees %q", tab, leak)
			}
		}
	}
	if v := get("v1", "?tab=diagnostics"); !strings.Contains(v, "HTTP status") || !strings.Contains(v, "visible to admins only") {
		t.Error("viewer diagnostics lack the failure kind or the note")
	}

	// An unknown tab is the overview; an unknown monitor is 404; HEAD has no body.
	if html := get("a1", "?tab=bogus"); !strings.Contains(html, `aria-current="page">Overview`) {
		t.Error("an unknown tab does not fall back to the overview")
	}
	if rec := e.getAs(t, "a1", "GET", "/monitors/nope"); rec.Code != 404 {
		t.Errorf("unknown monitor = %d", rec.Code)
	}
	if rec := e.getAs(t, "v1", "HEAD", "/monitors/"+id); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if rec := e.serve(req("GET", "/monitors/"+id, nil)); rec.Code != 303 {
		t.Errorf("anonymous = %d", rec.Code)
	}
}

func TestPauseResumeDelete(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	tg := newCountingTarget(t)
	events := e.subscribe(t)
	rec := e.postAs(t, "a1", "/monitors", url.Values{"name": {"API"}, "url": {tg.URL}, "enabled": {"1"}})
	if rec.Code != 303 {
		t.Fatalf("create = %d", rec.Code)
	}
	id := e.monitorID(t, "API")
	ctx := context.Background()
	state := func() store.Monitor {
		m, err := store.GetMonitor(ctx, e.db.Reader, id)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	audits := func(typ string) int {
		return e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = ? AND object_id = ? AND user_id = 'a1'`, typ, id)
	}

	// Viewers cannot act.
	for _, action := range []string{"pause", "resume", "delete"} {
		if rec := e.postAs(t, "v1", "/monitors/"+id+"/"+action, url.Values{"confirm": {"1"}}); rec.Code != 403 {
			t.Errorf("viewer %s = %d", action, rec.Code)
		}
	}

	// Pause, twice: one change, one audit event.
	for range 2 {
		if rec := e.postAs(t, "a1", "/monitors/"+id+"/pause", nil); rec.Code != 303 || rec.Header().Get("Location") != "/monitors/"+id {
			t.Fatalf("pause = %d %q", rec.Code, rec.Header().Get("Location"))
		}
	}
	if m := state(); m.State != "paused" || m.Enabled || audits("monitor.paused") != 1 {
		t.Fatalf("after pause: %s enabled=%v, %d audit events", m.State, m.Enabled, audits("monitor.paused"))
	}
	if html := e.getAs(t, "a1", "GET", "/monitors/"+id).Body.String(); !strings.Contains(html, "/resume") || strings.Contains(html, "/pause\"") {
		t.Error("a paused monitor does not offer Resume")
	}

	// Resume checks at once.
	hits := tg.hits.Load()
	if rec := e.postAs(t, "a1", "/monitors/"+id+"/resume", nil); rec.Code != 303 {
		t.Fatalf("resume = %d", rec.Code)
	}
	waitUntil(t, "a check after resume", func() bool { return tg.hits.Load() > hits })
	if audits("monitor.resumed") != 1 {
		t.Error("resume not audited")
	}

	// Delete asks first and writes nothing.
	rec = e.postAs(t, "a1", "/monitors/"+id+"/delete", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Delete API?") || !strings.Contains(rec.Body.String(), `name="confirm" value="1"`) {
		t.Fatalf("delete without confirm = %d", rec.Code)
	}
	state()
	rec = e.postAs(t, "a1", "/monitors/"+id+"/delete", url.Values{"confirm": {"1"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/monitors" {
		t.Fatalf("delete = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if _, err := store.GetMonitor(ctx, e.db.Reader, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if audits("monitor.deleted") != 1 {
		t.Error("delete not audited")
	}
	waitUntil(t, "monitor.deleted", func() bool {
		return strings.Contains(events(), "event: monitor.deleted\ndata: {\"monitor_id\":\""+id+"\"}")
	})
	// Audit events outlive the monitor they describe.
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE object_id = ?`, id); n != 4 {
		t.Errorf("%d audit events for the deleted monitor, want 4", n)
	}
	for _, action := range []string{"pause", "resume", "delete"} {
		if rec := e.postAs(t, "a1", "/monitors/"+id+"/"+action, url.Values{"confirm": {"1"}}); rec.Code != 404 {
			t.Errorf("%s of a deleted monitor = %d", action, rec.Code)
		}
	}
}

func TestMonitorListFragment(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	e.addMonitor(t, "b", "https://b.example")
	e.addMonitor(t, "A", "https://a.example")
	rec := e.getAs(t, "a1", "GET", "/fragments/monitors")
	html := rec.Body.String()
	if rec.Code != 200 || strings.Contains(html, "<html") || !strings.Contains(html, `data-live-list`) ||
		strings.Index(html, ">A<") > strings.Index(html, ">b<") || !strings.Contains(html, "https://a.example") {
		t.Fatalf("admin fragment = %d:\n%s", rec.Code, html)
	}
	if v := e.getAs(t, "v1", "GET", "/fragments/monitors").Body.String(); strings.Contains(v, "example") {
		t.Error("the viewer's list shows addresses")
	}
	if rec := e.serve(req("GET", "/fragments/monitors", nil)); rec.Code != 303 {
		t.Errorf("anonymous = %d", rec.Code)
	}
	// The list page uses the same element, so it can replace itself.
	if page := e.getAs(t, "a1", "GET", "/monitors").Body.String(); !strings.Contains(page, `hx-get="/fragments/monitors"`) {
		t.Error("the list page is not a live list")
	}
}

func TestOverviewFacts(t *testing.T) {
	later := monitorsNow.Add(-90 * time.Second)
	expiry := monitorsNow.Add(10*24*time.Hour + time.Hour)
	fs := overviewFacts(store.Monitor{IntervalSeconds: 60, LastSuccessAt: &later, TLSNotAfter: &expiry, CreatedAt: monitorsNow}, monitorsNow)
	got := map[string]string{}
	for _, f := range fs {
		got[f.Label] = f.Value
	}
	if got["Checked every"] != "1m" || got["Last success"] != "1m ago" || got["Last failure"] != "never" ||
		got["Certificate expires"] != "2026-10-16 (in 10 days)" || got["Created"] != "2026-10-06" {
		t.Errorf("facts = %v", got)
	}
	past := monitorsNow.Add(-time.Hour)
	if fs := overviewFacts(store.Monitor{TLSNotAfter: &past}, monitorsNow); !strings.Contains(fs[3].Value, "expired") {
		t.Errorf("expired certificate: %v", fs[3])
	}
}
