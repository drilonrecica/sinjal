package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drilonrecica/sinjal/internal/store"
)

// apiAs sends an API request as a signed-in user. headers are added as
// given, so a test can leave APIHeader out.
func (e *appEnv) apiAs(t *testing.T, userID, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	cookie, _ := e.signIn(t, userID)
	r := withCookie(req(method, path, nil), cookie)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return e.serve(r)
}

var writeHeaders = map[string]string{APIHeader: apiHeaderValue}

func apiErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Message == "" {
		t.Fatalf("not the error format (%v): %s", err, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("error Content-Type = %q", ct)
	}
	return body.Error.Code
}

func TestAPIRead(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	up := e.addMonitor(t, "Website", "https://private.corp.internal/path?token=s3cret")
	down := e.addMonitor(t, "Database", "https://db.example.com/")
	e.exec(t, `UPDATE monitors SET current_state = 'up' WHERE id = ?`, up)
	e.exec(t, `UPDATE monitors SET current_state = 'down' WHERE id = ?`, down)
	e.addFailure(t, down, monitorsNow, "connect", privateSummary, privateSnippet)

	for _, user := range []string{"a1", "v1"} {
		rec := e.apiAs(t, user, "GET", "/api/v1/status", nil)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s: status = %d %v", user, rec.Code, rec.Header())
		}
		var st struct {
			Status   string
			Monitors int
			Counts   map[string]int
			Problems []apiMonitor
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		if st.Status != "down" || st.Monitors != 2 || st.Counts["up"] != 1 || st.Counts["down"] != 1 || len(st.Problems) != 1 || st.Problems[0].ID != down {
			t.Errorf("%s: status = %+v", user, st)
		}

		rec = e.apiAs(t, user, "GET", "/api/v1/monitors", nil)
		var list struct{ Monitors []apiMonitor }
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Monitors) != 2 || list.Monitors[0].Name != "Database" {
			t.Fatalf("%s: list = %d %s", user, rec.Code, rec.Body.String())
		}

		rec = e.apiAs(t, user, "GET", "/api/v1/monitors/"+up, nil)
		var one apiMonitor
		if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil || one.ID != up || one.State != "up" || !one.Enabled || one.Type != "http" {
			t.Fatalf("%s: monitor = %d %s", user, rec.Code, rec.Body.String())
		}
		for _, leak := range []string{"private.corp.internal", "s3cret", privateSummary, privateSnippet} {
			for _, p := range []string{"/api/v1/status", "/api/v1/monitors", "/api/v1/monitors/" + down} {
				if strings.Contains(e.apiAs(t, user, "GET", p, nil).Body.String(), leak) {
					t.Errorf("%s leaks %q", p, leak)
				}
			}
		}
	}

	rec := e.apiAs(t, "a1", "GET", "/api/v1/monitors/nope", nil)
	if rec.Code != 404 || apiErrorCode(t, rec) != "monitor_not_found" {
		t.Errorf("unknown monitor = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPIAnonymous(t *testing.T) {
	e := newAppEnv(t)
	id := e.addMonitor(t, "Website", "https://example.com/")
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/status"}, {"GET", "/api/v1/monitors"}, {"GET", "/api/v1/monitors/" + id},
		{"POST", "/api/v1/monitors/" + id + "/pause"}, {"POST", "/api/v1/monitors/" + id + "/resume"},
	} {
		r := req(c.method, c.path, nil)
		r.Header.Set(APIHeader, apiHeaderValue)
		rec := e.serve(r)
		if rec.Code != 401 || apiErrorCode(t, rec) != "unauthenticated" || rec.Header().Get("Location") != "" {
			t.Errorf("anonymous %s %s = %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

func TestAPIPauseResume(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	id := e.addMonitor(t, "Website", "https://example.com/")
	state := func() store.Monitor {
		m, err := store.GetMonitor(t.Context(), e.db.Reader, id)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	audits := func(typ string) (n int) {
		e.db.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE event_type = ? AND object_id = ?`, typ, id).Scan(&n)
		return
	}

	// Refusals change nothing: no header, a cross-origin request, a viewer.
	pause := "/api/v1/monitors/" + id + "/pause"
	if rec := e.apiAs(t, "a1", "POST", pause, nil); rec.Code != 403 || apiErrorCode(t, rec) != "missing_header" {
		t.Errorf("no header = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.apiAs(t, "a1", "POST", pause, map[string]string{APIHeader: "1", "Origin": "https://evil.example"}); rec.Code != 403 || apiErrorCode(t, rec) != "cross_origin" {
		t.Errorf("cross-origin = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.apiAs(t, "a1", "POST", pause, map[string]string{APIHeader: "1", "Sec-Fetch-Site": "cross-site"}); rec.Code != 403 {
		t.Errorf("cross-site fetch = %d", rec.Code)
	}
	if rec := e.apiAs(t, "v1", "POST", pause, writeHeaders); rec.Code != 403 || apiErrorCode(t, rec) != "forbidden" {
		t.Errorf("viewer = %d %s", rec.Code, rec.Body.String())
	}
	if state().State == "paused" || audits("monitor.paused") != 0 {
		t.Fatal("a refused request paused the monitor")
	}

	// A same-origin browser request with the header works, and so does a
	// script without Origin.
	h := map[string]string{APIHeader: "1", "Origin": "http://example.com"}
	if rec := e.apiAs(t, "a1", "POST", pause, h); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("pause = %d %s", rec.Code, rec.Body.String())
	}
	if m := state(); m.State != "paused" || m.Enabled || audits("monitor.paused") != 1 {
		t.Fatalf("after pause: %s enabled=%v, %d audit events", m.State, m.Enabled, audits("monitor.paused"))
	}
	// Idempotent: no second audit event.
	if rec := e.apiAs(t, "a1", "POST", pause, writeHeaders); rec.Code != http.StatusNoContent || audits("monitor.paused") != 1 {
		t.Errorf("second pause = %d, %d events", rec.Code, audits("monitor.paused"))
	}
	if got := e.apiAs(t, "v1", "GET", "/api/v1/monitors/"+id, nil).Body.String(); !strings.Contains(got, `"state":"paused"`) {
		t.Errorf("monitor after pause: %s", got)
	}
	if rec := e.apiAs(t, "a1", "POST", "/api/v1/monitors/"+id+"/resume", writeHeaders); rec.Code != http.StatusNoContent {
		t.Fatalf("resume = %d", rec.Code)
	}
	if m := state(); m.State == "paused" || !m.Enabled || audits("monitor.resumed") != 1 {
		t.Fatalf("after resume: %s enabled=%v, %d audit events", m.State, m.Enabled, audits("monitor.resumed"))
	}

	rec := e.apiAs(t, "a1", "POST", "/api/v1/monitors/nope/pause", writeHeaders)
	if rec.Code != 404 || apiErrorCode(t, rec) != "monitor_not_found" {
		t.Errorf("unknown = %d %s", rec.Code, rec.Body.String())
	}
}

// The API is for the instance's own host: it is not served on a mapped
// status page hostname (hosts_test walks every route to prove it).
func TestAPINotOnMappedHost(t *testing.T) {
	e := newAppEnv(t)
	e.mappedPage(t, store.StatusPageInput{Slug: "shop", Title: "Shop"})
	if rec := e.serve(onHost(req("GET", "/api/v1/status", nil), mappedHost)); rec.Code != 404 {
		t.Errorf("/api/v1/status on a mapped host = %d", rec.Code)
	}
}
