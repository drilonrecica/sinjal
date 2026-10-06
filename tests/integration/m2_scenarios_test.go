package integration

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/store"
)

// M2-19: the monitoring scenarios of docs/20 that M2 owns, end to end in
// the real binary against one local target. Unit and package tests cover
// the edge cases; these prove the shipped wiring (scheduler, workers,
// HTTP checks, result processor, store) behaves as specified.

// result is one stored check_results row.
type result struct {
	at      time.Time
	success bool
	status  string
	kind    string
	message string
	snippet string
	ms      float64
}

func checkResults(t *testing.T, d *db.DB, id string) []result {
	t.Helper()
	rows, err := d.Reader.Query(`SELECT checked_at, success, COALESCE(protocol_status, ''), COALESCE(error_kind, ''),
		COALESCE(error_message, ''), COALESCE(diagnostic_snippet, ''), COALESCE(duration_ms, 0)
		FROM check_results WHERE monitor_id = ? ORDER BY checked_at, id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []result
	for rows.Next() {
		var r result
		var at string
		if err := rows.Scan(&at, &r.success, &r.status, &r.kind, &r.message, &r.snippet, &r.ms); err != nil {
			t.Fatal(err)
		}
		r.at, _ = time.Parse(time.RFC3339, at)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// seed creates an enabled HTTP monitor in a stopped server's database.
func seed(t *testing.T, d *db.DB, name, url string, edit func(*store.MonitorInput)) string {
	t.Helper()
	in := store.MonitorInput{Name: name, Enabled: true, RetryDelayMS: 300,
		HTTP: store.HTTPConfig{URL: url, FollowRedirects: true}}
	if edit != nil {
		edit(&in)
	}
	id, err := store.CreateMonitor(context.Background(), d, in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// scenarioTarget serves one path per scenario.
type scenarioTarget struct {
	*httptest.Server
	flakyHits   atomic.Int64 // /flaky: the first request fails
	recovered   atomic.Bool  // /recover fails until set
	bigWritten  atomic.Int64 // bytes /big managed to write
	hangEntered atomic.Int64
	hangGone    atomic.Int64 // /hang requests the client cancelled
}

func newScenarioTarget(t *testing.T) *scenarioTarget {
	t.Helper()
	tg := &scenarioTarget{}
	mux := http.NewServeMux()
	mux.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `<h1>maintenance</h1>`)
	})
	mux.HandleFunc("/flaky", func(w http.ResponseWriter, r *http.Request) {
		if tg.flakyHits.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
		}
	})
	mux.HandleFunc("/recover", func(w http.ResponseWriter, r *http.Request) {
		if !tg.recovered.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	// 16 MiB, the match at the very end: far past the 1 MiB read cap.
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		chunk := []byte(strings.Repeat("x", 64<<10))
		for range 256 {
			n, err := w.Write(chunk)
			tg.bigWritten.Add(int64(n))
			if err != nil {
				return
			}
		}
		fmt.Fprint(w, "NEEDLE")
	})
	mux.HandleFunc("/r/{n}", func(w http.ResponseWriter, r *http.Request) {
		var n int
		fmt.Sscan(r.PathValue("n"), &n)
		if n <= 1 {
			http.Redirect(w, r, "/ok", http.StatusFound)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/r/%d", n-1), http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ok","n":42,"items":[{"id":"a"}]}`)
	})
	mux.HandleFunc("/notjson", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html>not json</html>`)
	})
	mux.HandleFunc("/hang", func(w http.ResponseWriter, r *http.Request) {
		tg.hangEntered.Add(1)
		select {
		case <-r.Context().Done():
			tg.hangGone.Add(1)
		case <-time.After(30 * time.Second):
		}
	})
	tg.Server = httptest.NewServer(mux)
	t.Cleanup(tg.Close)
	return tg
}

func TestMonitoringScenarios(t *testing.T) {
	skipShort(t)
	tg := newScenarioTarget(t)
	dir := t.TempDir()
	if err := start(t, dir).stop(); err != nil { // creates and migrates the database
		t.Fatal(err)
	}
	d := openDB(t, dir)

	// Scenarios 1-3 (state level).
	failing := seed(t, d, "1 failure retry down", tg.URL+"/fail", nil)
	flaky := seed(t, d, "2 retry success", tg.URL+"/flaky", nil)
	recovering := seed(t, d, "3 down then recovery", tg.URL+"/recover", nil)
	// 14 body cap.
	big := seed(t, d, "14 body cap", tg.URL+"/big", func(m *store.MonitorInput) { m.HTTP.BodyContains = "NEEDLE" })
	// 15 redirects.
	chain := seed(t, d, "15 chain", tg.URL+"/r/3", func(m *store.MonitorInput) { m.HTTP.ExpectedStatus = "200" })
	noFollow := seed(t, d, "15 not followed", tg.URL+"/r/1", func(m *store.MonitorInput) {
		m.HTTP.FollowRedirects = false
		m.HTTP.ExpectedStatus = "200"
	})
	loop := seed(t, d, "15 loop", tg.URL+"/loop", nil)
	// 16 timeout.
	hang := seed(t, d, "16 timeout", tg.URL+"/hang", func(m *store.MonitorInput) { m.TimeoutMS = 1000 })
	// Assertion failures.
	status := seed(t, d, "status", tg.URL+"/ok", func(m *store.MonitorInput) { m.HTTP.ExpectedStatus = "201" })
	contains := seed(t, d, "contains", tg.URL+"/ok", func(m *store.MonitorInput) { m.HTTP.BodyContains = "healthy" })
	notContains := seed(t, d, "not contains", tg.URL+"/ok", func(m *store.MonitorInput) { m.HTTP.BodyNotContains = `"ok"` })
	jsonEq := seed(t, d, "json equals", tg.URL+"/ok", func(m *store.MonitorInput) {
		m.HTTP.JSONAssertions = `[{"path":"$.items[0].id","op":"equals","value":"b"}]`
	})
	jsonOK := seed(t, d, "json passes", tg.URL+"/ok", func(m *store.MonitorInput) {
		m.HTTP.JSONAssertions = `[{"path":"$.status","op":"equals","value":"ok"},{"path":"$.n","op":"equals","value":42.0},{"path":"$.missing","op":"not_exists"}]`
	})
	notJSON := seed(t, d, "json parse", tg.URL+"/notjson", func(m *store.MonitorInput) {
		m.HTTP.JSONAssertions = `[{"path":"$.status","op":"exists"}]`
	})
	// Scenario 3 needs a check after the target recovers: a one-second
	// interval (validation allows nothing below ten).
	if _, err := d.Writer.Exec(`UPDATE monitors SET interval_seconds = 1 WHERE id = ?`, recovering); err != nil {
		t.Fatal(err)
	}

	// Watch scenario 2 for a DOWN that must never happen.
	watchCtx, stopWatch := context.WithCancel(context.Background())
	var flakySeen sync.Map
	var watching sync.WaitGroup
	watching.Go(func() {
		for watchCtx.Err() == nil {
			var st string
			if err := d.Reader.QueryRow(`SELECT current_state FROM monitors WHERE id = ?`, flaky).Scan(&st); err == nil {
				flakySeen.Store(st, true)
			}
			time.Sleep(2 * time.Millisecond)
		}
	})

	s := start(t, dir)
	state := func(id string) string { return monitorRow(t, d, id).State }
	stored := func(id string, n int) func() bool {
		return func() bool { return count(t, d, `SELECT COUNT(*) FROM check_results WHERE monitor_id = ?`, id) >= n }
	}

	// 1: failure, confirmation retry after the retry delay, DOWN.
	waitFor(t, s, "scenario 1 down", func() bool { return state(failing) == "down" })
	rs := checkResults(t, d, failing)
	if len(rs) != 2 || rs[0].success || rs[1].success || rs[0].status != "503" || rs[0].kind != "http_status" {
		t.Fatalf("scenario 1 results: %+v", rs)
	}
	if !strings.Contains(rs[0].snippet, "<h1>maintenance</h1>") {
		t.Errorf("scenario 1 snippet = %q", rs[0].snippet)
	}

	// 2: the retry succeeds: UP, never DOWN.
	waitFor(t, s, "scenario 2 up", func() bool { return state(flaky) == "up" })
	stopWatch()
	watching.Wait()
	if _, down := flakySeen.Load("down"); down {
		t.Error("scenario 2: the monitor was DOWN although the retry succeeded")
	}
	if rs := checkResults(t, d, flaky); len(rs) != 2 || rs[0].success || !rs[1].success {
		t.Errorf("scenario 2 results: %+v", rs)
	}

	// 3: DOWN, then the target recovers: UP since the recovering check.
	waitFor(t, s, "scenario 3 down", func() bool { return state(recovering) == "down" })
	downSince := monitorRow(t, d, recovering).StateSince
	tg.recovered.Store(true)
	waitFor(t, s, "scenario 3 up", func() bool { return state(recovering) == "up" })
	m := monitorRow(t, d, recovering)
	rs = checkResults(t, d, recovering)
	last := rs[len(rs)-1]
	// Times are stored to the second, so the recovery may share its second
	// with the outage's start; it can never precede it.
	if !last.success || !m.StateSince.Equal(last.at) || m.StateSince.Before(downSince) || m.LastSuccessAt == nil {
		t.Errorf("scenario 3: up since %v (down since %v), last result %+v", m.StateSince, downSince, last)
	}
	if n := len(rs); n < 3 || rs[n-2].success || rs[n-3].success {
		t.Errorf("scenario 3: no outage before the recovery: %+v", rs)
	}

	// 14: the match lies past the cap, so it is not found, and the check
	// stopped reading: the target could not write the whole body.
	waitFor(t, s, "scenario 14 result", stored(big, 1))
	rs = checkResults(t, d, big)
	if rs[0].success || rs[0].kind != "body_assertion" || len(rs[0].snippet) > 4096 {
		t.Errorf("scenario 14: %+v (snippet %d bytes)", rs[0].kind, len(rs[0].snippet))
	}
	waitFor(t, s, "the target to give up writing", func() bool {
		return tg.bigWritten.Load() > 0 && tg.bigWritten.Load() < 16<<20
	})

	// 15: a chain within the limit is followed; without following, the
	// redirect is judged by its own status; a loop stops at the limit.
	waitFor(t, s, "scenario 15 results", func() bool {
		return stored(chain, 1)() && stored(noFollow, 1)() && stored(loop, 1)()
	})
	if r := checkResults(t, d, chain)[0]; !r.success || r.status != "200" {
		t.Errorf("redirect chain: %+v", r)
	}
	if r := checkResults(t, d, noFollow)[0]; r.success || r.kind != "http_status" || r.status != "302" {
		t.Errorf("redirect not followed: %+v", r)
	}
	if r := checkResults(t, d, loop)[0]; r.success || r.kind != "protocol" || !strings.Contains(r.message, "10 redirects") {
		t.Errorf("redirect loop: %+v", r)
	}

	// 16: the hanging request is cut off at the timeout and the target
	// sees it cancelled.
	waitFor(t, s, "scenario 16 result", stored(hang, 1))
	r := checkResults(t, d, hang)[0]
	if r.success || r.kind != "timeout" || r.ms < 900 || r.ms > 3000 {
		t.Errorf("timeout: %+v", r)
	}
	waitFor(t, s, "the target to see the cancellation", func() bool { return tg.hangGone.Load() >= 1 })

	// Assertion failures, each with its own kind and a snippet saying why.
	for _, c := range []struct {
		id, kind, inSnippet string
	}{
		{status, "http_status", `"status":"ok"`},
		{contains, "body_assertion", `"status":"ok"`},
		{notContains, "body_assertion", `"status":"ok"`},
		{jsonEq, "json_assertion", `$.items[0].id`},
		{notJSON, "json_parse", "not json"},
	} {
		waitFor(t, s, "result of "+c.kind, stored(c.id, 1))
		r := checkResults(t, d, c.id)[0]
		if r.success || r.kind != c.kind || !strings.Contains(r.snippet, c.inSnippet) {
			t.Errorf("%s: %+v", monitorRow(t, d, c.id).Name, r)
		}
	}
	waitFor(t, s, "the passing JSON monitor", stored(jsonOK, 1))
	if r := checkResults(t, d, jsonOK)[0]; !r.success || r.snippet != "" {
		t.Errorf("passing JSON assertions: %+v", r)
	}
	// Successful bodies are never stored.
	if n := count(t, d, `SELECT COUNT(*) FROM check_results WHERE success = 1 AND diagnostic_snippet IS NOT NULL`); n != 0 {
		t.Errorf("%d successful checks stored a body", n)
	}

	if err := s.stop(); err != nil {
		t.Fatalf("exit after SIGTERM: %v\n%s", err, s.logs)
	}
}

// TestCreateMonitorThroughForm: a monitor created through the real form is
// announced on an open stream, checked at once, announced again with its
// result, audited, and gone with its history after a delete.
func TestCreateMonitorThroughForm(t *testing.T) {
	skipShort(t)
	var hits atomic.Int64
	var authSeen atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		authSeen.Store(r.Header.Get("Authorization"))
	}))
	defer target.Close()

	dir := t.TempDir()
	s := start(t, dir)
	createAdmin(t, s)
	c := signIn(t, s, "admin", adminPassword)
	d := openDB(t, dir)

	req, _ := http.NewRequest(http.MethodGet, s.base+"/events", nil)
	req.AddCookie(&http.Cookie{Name: "sinjal_session", Value: c.token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var mu sync.Mutex
	var stream strings.Builder
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			mu.Lock()
			stream.WriteString(sc.Text() + "\n")
			mu.Unlock()
		}
	}()
	streamed := func() string { mu.Lock(); defer mu.Unlock(); return stream.String() }

	const token = "form-bearer-token-9f8e7d"
	rs, body := c.post("/monitors", url.Values{
		"name": {"Through the form"}, "url": {target.URL + "/health"}, "enabled": {"1"},
		"auth": {"bearer"}, "bearer_token": {token}, "follow_redirects": {"1"},
	})
	if rs.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /monitors = %d:\n%s", rs.StatusCode, body)
	}
	id := strings.TrimPrefix(rs.Header.Get("Location"), "/monitors/")
	if _, err := store.GetMonitor(context.Background(), d.Reader, id); err != nil {
		t.Fatalf("created monitor %q: %v", id, err)
	}
	frame := func(event string) string {
		return "event: " + event + "\ndata: {\"monitor_id\":\"" + id + "\"}\n"
	}
	waitFor(t, s, "monitor.created", func() bool { return strings.Contains(streamed(), frame("monitor.created")) })
	waitFor(t, s, "the first check, announced", func() bool { return strings.Contains(streamed(), frame("monitor.updated")) })
	if hits.Load() < 1 || authSeen.Load() != "Bearer "+token {
		t.Errorf("target: %d requests, Authorization %q", hits.Load(), authSeen.Load())
	}
	if st := monitorRow(t, d, id).State; st != "up" {
		t.Errorf("state = %s", st)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'monitor.created' AND object_id = ?`, id); n != 1 {
		t.Errorf("audit events = %d", n)
	}

	// The detail page shows it; the delete asks, then removes everything.
	if rs, page := c.get("/monitors/" + id); rs.StatusCode != 200 || !strings.Contains(page, "Through the form") || strings.Contains(page, token) {
		t.Fatalf("detail = %d", rs.StatusCode)
	}
	if rs, _ := c.post("/monitors/"+id+"/delete", nil); rs.StatusCode != 200 {
		t.Fatalf("delete without confirm = %d", rs.StatusCode)
	}
	if rs, _ := c.post("/monitors/"+id+"/delete", url.Values{"confirm": {"1"}}); rs.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete = %d", rs.StatusCode)
	}
	waitFor(t, s, "monitor.deleted", func() bool { return strings.Contains(streamed(), frame("monitor.deleted")) })
	var n int
	if err := d.Reader.QueryRow(`SELECT COUNT(*) FROM check_results WHERE monitor_id = ?`, id).Scan(&n); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if n != 0 || count(t, d, `SELECT COUNT(*) FROM monitor_secrets WHERE monitor_id = ?`, id) != 0 {
		t.Errorf("%d results left after the delete", n)
	}
	notLogged(t, s, token)
	if err := s.stop(); err != nil {
		t.Fatalf("exit after SIGTERM: %v\n%s", err, s.logs)
	}
}
