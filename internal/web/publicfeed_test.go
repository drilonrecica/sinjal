package web

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/statuspage"
	"github.com/drilonrecica/sinjal/internal/store"
)

// feedFixture is a public page with a down service, an incident and a
// published note, all around the private values of the redaction tests.
func (e *appEnv) feedFixture(t *testing.T) (monitorID string) {
	t.Helper()
	db := e.addMonitor(t, privateName, "https://"+privateHost+":5432/health?token=s3cret")
	api := e.addMonitor(t, "api-internal", "http://"+privateIP+"/healthz")
	e.exec(t, `UPDATE monitors SET current_state = 'up'`)
	e.exec(t, `UPDATE monitors SET current_state = 'down' WHERE id = ?`, db)
	e.exec(t, `INSERT INTO check_results (monitor_id, checked_at, duration_ms, success) VALUES (?, ?, 87, 1)`, api, store.FormatTime(time.Now()))
	e.addIncident(t, "inc-secret-id", db, time.Now().Add(-time.Hour), nil, false)
	e.exec(t, `UPDATE incidents SET summary = ? WHERE id = 'inc-secret-id'`, privateSummary)
	if _, err := store.AddIncidentNote(context.Background(), e.db, "inc-secret-id", "We are restoring the database.", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.addPage(t, store.StatusPageInput{Slug: "main", Title: "Acme status", Description: "Everything we run", Groups: []string{"Web"},
		Monitors: []store.StatusPageMonitorInput{
			{MonitorID: api, Group: "Web", DisplayName: "Public API", ShowLatency: true, Sort: 1},
			{MonitorID: db, DisplayName: "Database", Sort: 2},
		}})
	return db
}

func TestPageJSON(t *testing.T) {
	e := newAppEnv(t)
	dbID := e.feedFixture(t)
	rec := e.serve(req("GET", "/status/main/api.json", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("api.json = %d %v", rec.Code, rec.Header())
	}
	body := rec.Body.String()
	for _, secret := range []string{privateName, privateHost, privateIP, "s3cret", privateSummary, dbID, "inc-secret-id", "api-internal"} {
		if strings.Contains(body, secret) {
			t.Errorf("api.json leaks %q", secret)
		}
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if keys := mapKeys(got); keys != "components,generated_at,incidents,overall_status,page,uptime_90d" {
		t.Errorf("top-level fields = %s", keys)
	}
	comps := got["components"].([]any)
	if len(comps) != 2 {
		t.Fatalf("components = %v", comps)
	}
	first, second := comps[0].(map[string]any), comps[1].(map[string]any)
	if mapKeys(first) != "group,key,latency_ms,name,status,uptime_90d" || first["name"] != "Public API" || first["latency_ms"] != float64(87) || first["group"] != "Web" {
		t.Errorf("first component = %v", first)
	}
	if mapKeys(second) != "key,name,status,uptime_90d" || second["status"] != "down" {
		t.Errorf("second component = %v", second)
	}
	inc := got["incidents"].([]any)[0].(map[string]any)
	if mapKeys(inc) != "active,component,key,notes,started_at" || inc["component"] != second["key"] || inc["active"] != true {
		t.Errorf("incident = %v", inc)
	}
	if got["overall_status"].(map[string]any)["state"] != "partial" {
		t.Errorf("overall = %v", got["overall_status"])
	}

	// Keys are stable between reads and differ between pages.
	again := e.serve(req("GET", "/status/main/api.json", nil)).Body.String()
	if again != body {
		t.Error("two reads differ")
	}
	e.addPage(t, store.StatusPageInput{Slug: "second", Title: "Second",
		Monitors: []store.StatusPageMonitorInput{{MonitorID: dbID, DisplayName: "Database"}}})
	var other map[string]any
	json.Unmarshal(e.serve(req("GET", "/status/second/api.json", nil)).Body.Bytes(), &other)
	if other["components"].([]any)[0].(map[string]any)["key"] == second["key"] {
		t.Error("one monitor has the same key on two pages")
	}
}

func mapKeys(m map[string]any) string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return strings.Join(keys, ",")
}

func TestPageFeed(t *testing.T) {
	e := newAppEnv(t)
	dbID := e.feedFixture(t)
	rec := e.serve(req("GET", "/status/main/feed.xml", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/atom+xml") {
		t.Fatalf("feed.xml = %d %v", rec.Code, rec.Header())
	}
	body := rec.Body.String()
	for _, secret := range []string{privateName, privateHost, privateIP, "s3cret", privateSummary, dbID, "inc-secret-id"} {
		if strings.Contains(body, secret) {
			t.Errorf("feed leaks %q", secret)
		}
	}
	var f atomFeed
	if err := xml.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatalf("not XML: %v\n%s", err, body)
	}
	if f.Title != "Acme status incidents" || len(f.Entries) != 1 || f.Entries[0].Title != "Database: ongoing" ||
		!strings.Contains(f.Entries[0].Content.Text, "We are restoring the database.") || f.Link.Href != "http://example.com/status/main/feed.xml" {
		t.Errorf("feed = %+v", f)
	}
	if _, err := time.Parse(time.RFC3339, f.Updated); err != nil {
		t.Errorf("updated %q", f.Updated)
	}
}

func TestPageFeedAccess(t *testing.T) {
	for _, path := range []string{"/api.json", "/feed.xml"} {
		e := newAppEnv(t)
		e.passwordPage(t, "partners")
		hold := func(p string) {
			rec := e.serve(req("GET", "/status/partners"+p, nil))
			if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "Partner API") || strings.Contains(rec.Body.String(), "<form") {
				t.Errorf("locked %s = %d\n%s", p, rec.Code, rec.Body.String())
			}
		}
		hold(path)
		c := pageCookieOf(e.unlock("/status/partners", pagePassword, ""))
		r := req("GET", "/status/partners"+path, nil)
		r.AddCookie(c)
		if rec := e.serve(r); rec.Code != 200 {
			t.Errorf("unlocked %s = %d", path, rec.Code)
		}
		if rec := e.serve(req("POST", "/status/partners"+path, nil)); rec.Code == 200 {
			t.Errorf("POST %s = 200", path)
		}

		// Authenticated.
		m := e.addMonitor(t, "m-auth", "https://x.example.com/")
		e.addPage(t, store.StatusPageInput{Slug: "internal", Title: "Internal", Visibility: "authenticated",
			Monitors: []store.StatusPageMonitorInput{{MonitorID: m, DisplayName: "Internal API"}}})
		rec := e.serve(req("GET", "/status/internal"+path, nil))
		if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "Internal API") {
			t.Errorf("anonymous %s = %d", path, rec.Code)
		}
		e.addUser(t, "u1", "viewer", "viewer", "")
		token, _ := e.signIn(t, "u1")
		if rec := e.serve(withCookie(req("GET", "/status/internal"+path, nil), token)); rec.Code != 200 {
			t.Errorf("signed in %s = %d", path, rec.Code)
		}

		// Unlisted: only at its token, never under the slug.
		tok, hash, _ := statuspage.NewToken()
		m2 := e.addMonitor(t, "m-unl", "https://y.example.com/")
		e.addPage(t, store.StatusPageInput{Slug: "hidden", Title: "Hidden", Visibility: "unlisted", TokenHash: hash,
			Monitors: []store.StatusPageMonitorInput{{MonitorID: m2, DisplayName: "Hidden API"}}})
		if rec := e.serve(req("GET", "/status/hidden"+path, nil)); rec.Code != 404 {
			t.Errorf("unlisted by slug %s = %d", path, rec.Code)
		}
		rec = e.serve(req("GET", "/s/"+tok+path, nil))
		if rec.Code != 200 || rec.Header().Get("X-Robots-Tag") != "noindex, nofollow" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("unlisted by token %s = %d %v", path, rec.Code, rec.Header())
		}
		if rec := e.serve(req("GET", "/s/"+tok+"x"+path, nil)); rec.Code != 404 {
			t.Errorf("wrong token %s = %d", path, rec.Code)
		}
	}
}

func TestMappedHostFeeds(t *testing.T) {
	e := newAppEnv(t)
	e.mappedPage(t, store.StatusPageInput{Slug: "shop", Title: "Shop"})
	for path, ct := range map[string]string{"/api.json": "application/json", "/feed.xml": "application/atom+xml"} {
		rec := e.serve(onHost(req("GET", path, nil), mappedHost))
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), ct) || !strings.Contains(rec.Body.String(), "Shop") {
			t.Errorf("%s on the mapped host = %d %v", path, rec.Code, rec.Header())
		}
		// Not on the instance's own host.
		if rec := e.serve(req("GET", path, nil)); rec.Code == 200 {
			t.Errorf("%s on the base host = 200", path)
		}
	}

	pw := newAppEnv(t)
	pw.mappedPage(t, store.StatusPageInput{Slug: "closed", Title: "Closed", Visibility: "authenticated"})
	for _, path := range []string{"/api.json", "/feed.xml"} {
		if rec := pw.serve(onHost(req("GET", path, nil), mappedHost)); rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "Shop") {
			t.Errorf("authenticated %s on a mapped host = %d", path, rec.Code)
		}
	}
}

func TestPublicRefresh(t *testing.T) {
	e := newAppEnv(t)
	e.passwordPage(t, "partners")
	e.mappedPage(t, store.StatusPageInput{Slug: "shop", Title: "Shop"})
	const meta = `<meta http-equiv="refresh" content="60">`

	for _, r := range []*http.Request{req("GET", "/status/shop", nil), onHost(req("GET", "/", nil), mappedHost)} {
		rec := e.serve(r)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), meta) {
			t.Errorf("%s = %d, refresh missing", r.URL.Path, rec.Code)
		}
		// The page is script-free and never reads the admin stream.
		for _, bad := range []string{"<script", "/events", "hx-", "EventSource"} {
			if strings.Contains(rec.Body.String(), bad) {
				t.Errorf("public page contains %q", bad)
			}
		}
	}
	// A locked page does not reload itself: it would discard what was typed.
	if rec := e.serve(req("GET", "/status/partners", nil)); strings.Contains(rec.Body.String(), "refresh") {
		t.Error("the password form refreshes")
	}
	// Admin events are not served to the public.
	for _, host := range []string{"", mappedHost} {
		r := req("GET", "/events", nil)
		if host != "" {
			r = onHost(r, host)
		}
		if rec := e.serve(r); rec.Code == 200 {
			t.Errorf("/events on %q = 200 without a session", host)
		}
	}
}
