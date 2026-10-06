package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/store"
)

func TestCreateEachTypeThroughForm(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", testPassword)
	ctx := context.Background()

	for _, c := range []struct {
		typ  string
		form url.Values
	}{
		{"tcp", url.Values{"host": {"127.0.0.1"}, "port": {"1"}}},
		{"icmp", url.Values{"host": {"127.0.0.1"}}},
		{"dns", url.Values{"hostname": {"example.com"}, "query_type": {"MX"}, "resolver": {"127.0.0.1:1"},
			"expected": {"10 MX.example.com.\r\n\r\n10 mx.example.com\r\n"}, "match_mode": {"any"}}},
	} {
		page := e.getAs(t, "a1", "GET", "/monitors/new?type="+c.typ)
		if page.Code != 200 || !strings.Contains(page.Body.String(), `name="type" value="`+c.typ+`"`) ||
			!strings.Contains(page.Body.String(), `href="/monitors/new?type=`+c.typ+`" aria-current="page"`) {
			t.Fatalf("%s form = %d", c.typ, page.Code)
		}
		if strings.Contains(page.Body.String(), `id="url"`) || strings.Contains(page.Body.String(), "Advanced") {
			t.Errorf("%s form shows HTTP fields", c.typ)
		}
		c.form.Set("type", c.typ)
		c.form.Set("name", "m-"+c.typ)
		rec := e.postAs(t, "a1", "/monitors", c.form)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s create = %d: %s", c.typ, rec.Code, rec.Body.String())
		}
		id := e.monitorID(t, "m-"+c.typ)
		m, _ := store.GetMonitor(ctx, e.db.Reader, id)
		if m.Type != c.typ {
			t.Errorf("type = %s, want %s", m.Type, c.typ)
		}
	}
	if d, err := store.GetDNSConfig(ctx, e.db.Reader, e.monitorID(t, "m-dns")); err != nil ||
		strings.Join(d.Expected, "|") != "10 mx.example.com" || d.MatchMode != "any" || d.QueryType != "MX" {
		t.Errorf("dns config = %+v, %v", d, err)
	}

	// The list shows each target to admins only.
	list := e.getAs(t, "a1", "GET", "/monitors").Body.String()
	for _, want := range []string{"127.0.0.1:1", "example.com MX"} {
		if !strings.Contains(list, want) {
			t.Errorf("list misses target %q", want)
		}
	}
	e.addUser(t, "v1", "viewer", "viewer", testPassword)
	if list := e.getAs(t, "v1", "GET", "/monitors").Body.String(); strings.Contains(list, "127.0.0.1") || strings.Contains(list, "example.com MX") {
		t.Error("a viewer sees targets")
	}

	// Edit keeps the type whatever is posted and round-trips the config.
	id := e.monitorID(t, "m-tcp")
	edit := e.getAs(t, "a1", "GET", "/monitors/"+id+"/edit").Body.String()
	if !strings.Contains(edit, `value="127.0.0.1"`) || !strings.Contains(edit, `value="1"`) || strings.Contains(edit, `name="type"`) {
		t.Error("edit form does not show the stored TCP target or offers the type")
	}
	rec := e.postAs(t, "a1", "/monitors/"+id, url.Values{"type": {"http"}, "name": {"m-tcp"}, "host": {"db.internal"}, "port": {"5432"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit = %d: %s", rec.Code, rec.Body.String())
	}
	if c, _ := store.GetTCPConfig(ctx, e.db.Reader, id); c.Host != "db.internal" || c.Port != 5432 {
		t.Errorf("after edit = %+v", c)
	}

	// Configuration tab per type, admin only.
	cfg := e.getAs(t, "a1", "GET", "/monitors/"+e.monitorID(t, "m-dns")+"?tab=configuration").Body.String()
	for _, want := range []string{"Query", "Record type", "MX", "127.0.0.1:1", "At least one expected value"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("dns configuration misses %q", want)
		}
	}
	if cfg := e.getAs(t, "v1", "GET", "/monitors/"+e.monitorID(t, "m-dns")+"?tab=configuration").Body.String(); strings.Contains(cfg, "127.0.0.1:1") {
		t.Error("a viewer sees the resolver")
	}
}

func TestTypeFormErrors(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", testPassword)
	for _, c := range []struct {
		form   url.Values
		fields []string
	}{
		{url.Values{"type": {"tcp"}, "host": {"tcp://db"}, "port": {"x"}}, []string{"name", "host", "port"}},
		{url.Values{"type": {"icmp"}}, []string{"name", "host"}},
		{url.Values{"type": {"dns"}, "hostname": {"192.0.2.1"}, "query_type": {"SOA"}, "resolver": {"dns.google"}, "match_mode": {"x"}},
			[]string{"name", "hostname", "query_type", "resolver", "match_mode"}},
		{url.Values{"type": {"dns"}, "hostname": {"example.com"}, "query_type": {"A"}, "expected": {"2001:db8::1"}}, []string{"name", "expected"}},
		{url.Values{"type": {"heartbeat"}, "expected_interval": {"5"}, "grace": {"-1"}, "source_label": {strings.Repeat("x", 101)}},
			[]string{"name", "expected_interval", "grace", "source_label"}},
		{url.Values{"type": {"heartbeat"}, "expected_interval": {"soon"}}, []string{"name", "expected_interval"}},
	} {
		rec := e.postAs(t, "a1", "/monitors", c.form)
		body := rec.Body.String()
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%v = %d", c.form, rec.Code)
		}
		for _, f := range c.fields {
			if !strings.Contains(body, `href="#`+f+`"`) || !strings.Contains(body, `id="`+f+`-error"`) {
				t.Errorf("%s: no error for %s", c.form.Get("type"), f)
			}
		}
		if n := e.count(t, `SELECT COUNT(*) FROM monitors`); n != 0 {
			t.Fatalf("%d monitors written", n)
		}
	}
}

var pushURL = regexp.MustCompile(`http://example\.com/api/v1/heartbeat/([A-Za-z0-9_-]{43})`)

func TestHeartbeatCreateRevealsTokenOnce(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", testPassword)

	rec := e.postAs(t, "a1", "/monitors", url.Values{"type": {"heartbeat"}, "name": {"backup"},
		"expected_interval": {"3600"}, "grace": {"300"}, "source_label": {"db-1 backup"}, "enabled": {"1"}})
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create = %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	m := pushURL.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no push URL in %s", rec.Body.String())
	}
	token := m[1]
	if !strings.Contains(rec.Body.String(), "Bearer "+token) || !strings.Contains(rec.Body.String(), "/js/copy.") {
		t.Error("no bearer example or copy script")
	}
	id := e.monitorID(t, "backup")
	hash, _ := store.HashHeartbeatToken(token)
	if n := e.count(t, `SELECT COUNT(*) FROM heartbeat_monitor_config WHERE monitor_id = ? AND token_hash = ?`, id, hash); n != 1 {
		t.Error("stored hash does not match the revealed token")
	}
	c, _ := store.GetHeartbeatConfig(context.Background(), e.db.Reader, id)
	if c.ExpectedInterval != time.Hour || c.Grace != 5*time.Minute || c.SourceLabel != "db-1 backup" {
		t.Errorf("config = %+v", c)
	}

	// The token works, and is never shown or logged again.
	if beat := e.serve(req("POST", "/api/v1/heartbeat/"+token, nil)); beat.Code != http.StatusNoContent {
		t.Errorf("beat = %d", beat.Code)
	}
	for _, path := range []string{"/monitors/" + id, "/monitors/" + id + "?tab=configuration", "/monitors/" + id + "/edit", "/monitors"} {
		if body := e.getAs(t, "a1", "GET", path).Body.String(); strings.Contains(body, token) {
			t.Errorf("%s shows the token", path)
		}
	}
	if strings.Contains(e.logs.String(), token) {
		t.Error("token logged")
	}
	overview := e.getAs(t, "a1", "GET", "/monitors/"+id).Body.String()
	for _, want := range []string{"Expects a beat every", "1h, grace 5m", "Last beat", "Late after"} {
		if !strings.Contains(overview, want) {
			t.Errorf("overview misses %q", want)
		}
	}
	if list := e.getAs(t, "a1", "GET", "/monitors").Body.String(); !strings.Contains(list, "db-1 backup") {
		t.Error("list misses the source label")
	}

	// An edit keeps the token.
	rec = e.postAs(t, "a1", "/monitors/"+id, url.Values{"name": {"backup"}, "expected_interval": {"60"}})
	if rec.Code != http.StatusSeeOther || strings.Contains(rec.Body.String(), token) {
		t.Fatalf("edit = %d", rec.Code)
	}
	if beat := e.serve(req("POST", "/api/v1/heartbeat/"+token, nil)); beat.Code != http.StatusNoContent {
		t.Errorf("beat after edit = %d", beat.Code)
	}
}

func TestHeartbeatRegenerate(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", testPassword)
	e.addUser(t, "v1", "viewer", "viewer", testPassword)
	id, old := e.addHeartbeat(t)
	path := "/monitors/" + id + "/heartbeat/token"

	cfg := e.getAs(t, "a1", "GET", "/monitors/"+id+"?tab=configuration").Body.String()
	if !strings.Contains(cfg, `action="`+path+`"`) {
		t.Error("no regenerate button for an admin")
	}
	if cfg := e.getAs(t, "v1", "GET", "/monitors/"+id+"?tab=configuration").Body.String(); strings.Contains(cfg, path) {
		t.Error("regenerate button shown to a viewer")
	}
	if rec := e.postAs(t, "v1", path, nil); rec.Code != http.StatusForbidden {
		t.Errorf("viewer = %d", rec.Code)
	}

	// A stale session goes to re-authentication and nothing changes.
	cookie, sess := e.signIn(t, "a1")
	stale := time.Now().Add(-auth.ReauthWindow - time.Minute).UTC().Format(time.RFC3339)
	if _, err := e.db.Writer.Exec(`UPDATE sessions SET reauthenticated_at = ? WHERE id = ?`, stale, sess.ID); err != nil {
		t.Fatal(err)
	}
	r := withCookie(req("POST", path, url.Values{CSRFFormField: {e.csrf.token(sess.ID)}}), cookie)
	r.Header.Set("Referer", "http://example.com/monitors/"+id+"?tab=configuration")
	if rec := e.serve(r); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/reauth?next=") {
		t.Errorf("stale = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if beat := e.serve(req("POST", "/api/v1/heartbeat/"+old, nil)); beat.Code != http.StatusNoContent {
		t.Fatalf("old token after a refused regenerate = %d", beat.Code)
	}

	rec := e.postAs(t, "a1", path, nil)
	m := pushURL.FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || m == nil || m[1] == old || !strings.Contains(rec.Body.String(), "old one has stopped working") {
		t.Fatalf("regenerate = %d", rec.Code)
	}
	if beat := e.serve(req("POST", "/api/v1/heartbeat/"+old, nil)); beat.Code != http.StatusNotFound {
		t.Errorf("old token = %d", beat.Code)
	}
	if beat := e.serve(req("POST", "/api/v1/heartbeat/"+m[1], nil)); beat.Code != http.StatusNoContent {
		t.Errorf("new token = %d", beat.Code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'monitor.heartbeat_token_regenerated' AND object_id = ?`, id); n != 1 {
		t.Errorf("%d audit events", n)
	}
	if strings.Contains(e.logs.String(), m[1]) || strings.Contains(e.logs.String(), old) {
		t.Error("token logged")
	}

	// Only heartbeat monitors have a token.
	other := e.addMonitor(t, "web", "https://example.com")
	if rec := e.postAs(t, "a1", "/monitors/"+other+"/heartbeat/token", nil); rec.Code != http.StatusNotFound {
		t.Errorf("http monitor = %d", rec.Code)
	}
	if rec := e.postAs(t, "a1", "/monitors/missing/heartbeat/token", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown monitor = %d", rec.Code)
	}
}

func TestFailureKindsNamed(t *testing.T) {
	for kind, want := range map[string]string{
		"permission": "Permission", "dns_nxdomain": "No such name", "dns_no_answer": "No such record",
		"dns_mismatch": "Unexpected answer", "dns_error": "DNS error", "heartbeat_missed": "Missed heartbeat",
	} {
		if got := failureView(store.CheckResult{ErrorKind: kind}, false).Kind; got != want {
			t.Errorf("%s = %q, want %q", kind, got, want)
		}
	}
}
