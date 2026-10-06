package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// postAs posts a form with the session and CSRF token of an existing user.
func (e *appEnv) postAs(t *testing.T, userID, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	cookie, sess := e.signIn(t, userID)
	f := url.Values{CSRFFormField: {e.csrf.token(sess.ID)}}
	for k, v := range form {
		f[k] = v
	}
	return e.serve(withCookie(req("POST", path, f), cookie))
}

// countingTarget is a local server that records the Authorization and
// X-Api-Key headers of the last request.
type countingTarget struct {
	*httptest.Server
	hits         atomic.Int64
	auth, apiKey atomic.Value
}

func newCountingTarget(t *testing.T) *countingTarget {
	t.Helper()
	tg := &countingTarget{}
	tg.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tg.auth.Store(r.Header.Get("Authorization"))
		tg.apiKey.Store(r.Header.Get("X-Api-Key"))
		tg.hits.Add(1)
	}))
	t.Cleanup(tg.Close)
	return tg
}

// monitorID is the id of the monitor called name.
func (e *appEnv) monitorID(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := e.db.Reader.QueryRow(`SELECT id FROM monitors WHERE name = ?`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// subscribe opens an event stream on the hub and returns what it receives.
func (e *appEnv) subscribe(t *testing.T) func() string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.events.Serve(w, r, func() bool { return true })
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	var buf lockedBuffer
	go func() {
		defer resp.Body.Close()
		b := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(b)
			buf.Write(b[:n])
			if err != nil {
				return
			}
		}
	}()
	waitUntil(t, "the stream to open", func() bool { return strings.Contains(buf.String(), "retry:") })
	return buf.String
}

const testToken = "s3cr3t-bearer-token-value"

func TestCreateMonitor(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	tg := newCountingTarget(t)
	events := e.subscribe(t)

	page := e.getAs(t, "a1", "GET", "/monitors/new")
	if page.Code != 200 {
		t.Fatalf("new form = %d", page.Code)
	}
	for _, want := range []string{`action="/monitors"`, `name="interval" type="text" inputmode="decimal" value="30"`, `<details class="form-section form-advanced">`, "Create monitor"} {
		if !strings.Contains(page.Body.String(), want) {
			t.Errorf("new form lacks %q", want)
		}
	}

	rec := e.postAs(t, "a1", "/monitors", url.Values{
		"name": {"API"}, "url": {tg.URL + "/health"}, "tags": {"prod, api"}, "enabled": {"1"},
		"follow_redirects": {"1"}, "tls_expiry": {"1"}, "interval": {"60"}, "timeout": {"2.5"},
		"headers": {"Accept: application/json\nX-Trace: 1"},
		"auth":    {"bearer"}, "bearer_token": {testToken},
		"sh_new_name": {"X-Api-Key"}, "sh_new_value": {"key-value-123"},
		"json_path_0": {"$.status"}, "json_op_0": {"equals"}, "json_value_0": {"ok"},
		"json_path_1": {"$.n"}, "json_op_1": {"equals"}, "json_value_1": {"42"},
		"tls_warning_days": {"7, 30"}, "max_body_kib": {"64"},
	})
	id := e.monitorID(t, "API")
	if rec.Code != 303 || rec.Header().Get("Location") != "/monitors/"+id {
		t.Fatalf("create = %d %q:\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}

	ctx := context.Background()
	m, err := store.GetMonitor(ctx, e.db.Reader, id)
	if err != nil || m.Name != "API" || m.IntervalSeconds != 60 || m.TimeoutMS != 2500 || !m.Enabled || m.RetryDelayMS != store.DefaultRetryDelayMS {
		t.Fatalf("stored monitor %+v, %v", m, err)
	}
	c, _ := store.GetHTTPConfig(ctx, e.db.Reader, id)
	if c.Headers != `[{"name":"Accept","value":"application/json"},{"name":"X-Trace","value":"1"}]` ||
		c.JSONAssertions != `[{"path":"$.status","op":"equals","value":"ok"},{"path":"$.n","op":"equals","value":42}]` ||
		c.TLSWarningDays != "[30,7]" || c.MaxBodyBytes != 64<<10 || !c.FollowRedirects {
		t.Fatalf("stored config %+v", c)
	}
	if tags, _ := store.MonitorTags(ctx, e.db.Reader, id); strings.Join(tags, ",") != "api,prod" {
		t.Errorf("tags = %v", tags)
	}
	// Secrets are stored encrypted and reach the target.
	if n := e.count(t, `SELECT COUNT(*) FROM monitor_secrets WHERE monitor_id = ? AND CAST(value_enc AS TEXT) LIKE '%'||?||'%'`, id, testToken); n != 0 {
		t.Error("the token is stored in plain text")
	}
	waitUntil(t, "the first check", func() bool { return tg.hits.Load() > 0 })
	if tg.auth.Load() != "Bearer "+testToken || tg.apiKey.Load() != "key-value-123" {
		t.Errorf("target saw Authorization %q, X-Api-Key %q", tg.auth.Load(), tg.apiKey.Load())
	}
	waitUntil(t, "monitor.created", func() bool {
		return strings.Contains(events(), "event: monitor.created\ndata: {\"monitor_id\":\""+id+"\"}")
	})
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'monitor.created' AND object_id = ? AND user_id = 'a1'`, id); n != 1 {
		t.Errorf("audit events = %d", n)
	}
	if strings.Contains(e.logs.String(), testToken) || strings.Contains(e.logs.String(), "key-value-123") {
		t.Error("a secret reached the log")
	}

	// The edit form shows everything but the secrets.
	edit := e.getAs(t, "a1", "GET", "/monitors/"+id+"/edit").Body.String()
	for _, want := range []string{`value="API"`, `value="api, prod"`, "Accept: application/json\nX-Trace: 1", `value="2.5"`, `value="30, 7"`,
		`value="&#34;ok&#34;"`, `value="42"`, `<option value="bearer" selected>`, "X-Api-Key", "Leave blank to keep the current value", `action="/monitors/` + id + `"`} {
		if !strings.Contains(edit, want) {
			t.Errorf("edit form lacks %q", want)
		}
	}
	if !strings.Contains(edit, `id="json_path_4"`) || strings.Contains(edit, `id="json_path_5"`) {
		t.Error("two stored assertions should show as two rows plus three blank ones")
	}
	if strings.Contains(edit, testToken) || strings.Contains(edit, "key-value-123") {
		t.Error("the edit form shows a secret")
	}
}

func TestEditMonitorSecrets(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	tg := newCountingTarget(t)
	base := url.Values{"name": {"API"}, "url": {tg.URL}, "enabled": {"1"}}
	with := func(extra url.Values) url.Values {
		f := url.Values{}
		for k, v := range base {
			f[k] = v
		}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}
	rec := e.postAs(t, "a1", "/monitors", with(url.Values{"auth": {"basic"}, "basic_user": {"u"}, "basic_password": {"p1"},
		"sh_new_name": {"X-Api-Key"}, "sh_new_value": {"k1"}}))
	if rec.Code != 303 {
		t.Fatalf("create = %d", rec.Code)
	}
	id := e.monitorID(t, "API")
	secret := func(name string) string {
		v, err := store.GetSecret(context.Background(), e.db.Reader, e.key, id, name)
		if err != nil {
			return ""
		}
		return string(v)
	}
	if secret("auth.basic") != "u:p1" || secret("header.X-Api-Key") != "k1" {
		t.Fatalf("after create: %q %q", secret("auth.basic"), secret("header.X-Api-Key"))
	}

	// Blank keeps.
	if rec := e.postAs(t, "a1", "/monitors/"+id, with(url.Values{"auth": {"basic"}, "name": {"API 2"}})); rec.Code != 303 {
		t.Fatalf("edit = %d:\n%s", rec.Code, rec.Body)
	}
	if secret("auth.basic") != "u:p1" || secret("header.X-Api-Key") != "k1" {
		t.Fatal("a blank secret input changed the stored value")
	}
	// Filled replaces; switching to bearer drops basic; ticked removes.
	if rec := e.postAs(t, "a1", "/monitors/"+id, with(url.Values{"auth": {"bearer"}, "bearer_token": {"t1"},
		"sh_value.X-Api-Key": {"k2"}})); rec.Code != 303 {
		t.Fatalf("edit = %d:\n%s", rec.Code, rec.Body)
	}
	if secret("auth.basic") != "" || secret("auth.bearer") != "t1" || secret("header.X-Api-Key") != "k2" {
		t.Fatalf("after switching: basic %q bearer %q header %q", secret("auth.basic"), secret("auth.bearer"), secret("header.X-Api-Key"))
	}
	if rec := e.postAs(t, "a1", "/monitors/"+id, with(url.Values{"sh_remove.X-Api-Key": {"1"}})); rec.Code != 303 {
		t.Fatalf("edit = %d", rec.Code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM monitor_secrets WHERE monitor_id = ?`, id); n != 0 {
		t.Fatalf("%d secrets left after choosing no auth and removing the header", n)
	}
	// Choosing bearer with nothing stored and a blank token is an error.
	rec = e.postAs(t, "a1", "/monitors/"+id, with(url.Values{"auth": {"bearer"}}))
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Enter the token.") {
		t.Fatalf("bearer without token = %d", rec.Code)
	}
	if m, _ := store.GetMonitor(context.Background(), e.db.Reader, id); m.Name != "API" {
		t.Errorf("name = %q after the edits", m.Name)
	}
}

func TestMonitorFormErrors(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	rec := e.postAs(t, "a1", "/monitors", url.Values{
		"name": {""}, "url": {"ftp://x"}, "interval": {"abc"}, "timeout": {"60"}, "headers": {"no colon"},
		"tags": {"ok"}, "proxy_url": {"gopher://p"}, "json_path_0": {"status"}, "json_op_0": {"equals"}, "json_value_0": {"1"},
		"auth": {"bearer"}, "bearer_token": {testToken}, "max_body_kib": {"4096"},
	})
	body := rec.Body.String()
	if rec.Code != 422 {
		t.Fatalf("invalid create = %d", rec.Code)
	}
	for _, field := range []string{"name", "url", "interval", "headers", "json_assertions", "max_body_kib", "proxy_url"} {
		if !strings.Contains(body, `href="#`+field+`"`) {
			t.Errorf("summary lacks %s", field)
		}
	}
	for _, want := range []string{`id="name-error"`, `aria-invalid="true"`, `value="ftp://x"`, `value="abc"`, "no colon",
		`<details class="form-section form-advanced" open>`, `value="status"`} {
		if !strings.Contains(body, want) {
			t.Errorf("error page lacks %q", want)
		}
	}
	if strings.Contains(body, testToken) {
		t.Error("the error page echoes the token")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM monitors`); n != 0 {
		t.Fatalf("%d monitors written", n)
	}
	// Only the store can tell: the timeout must be under the interval.
	rec = e.postAs(t, "a1", "/monitors", url.Values{"name": {"x"}, "url": {"https://example.com"}, "interval": {"10"}, "timeout": {"10"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), `id="timeout-error"`) {
		t.Fatalf("timeout = interval: %d", rec.Code)
	}
}

func TestMonitorFormAccess(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	id := e.addMonitor(t, "API", "https://api.example.com")

	for _, path := range []string{"/monitors/new", "/monitors/" + id + "/edit"} {
		if rec := e.getAs(t, "v1", "GET", path); rec.Code != 403 {
			t.Errorf("viewer GET %s = %d", path, rec.Code)
		}
		if rec := e.serve(req("GET", path, nil)); rec.Code != 303 {
			t.Errorf("anonymous GET %s = %d", path, rec.Code)
		}
	}
	for _, path := range []string{"/monitors", "/monitors/" + id} {
		if rec := e.postAs(t, "v1", path, url.Values{"name": {"x"}, "url": {"https://x.example"}}); rec.Code != 403 {
			t.Errorf("viewer POST %s = %d", path, rec.Code)
		}
	}
	if rec := e.getAs(t, "a1", "GET", "/monitors/nope/edit"); rec.Code != 404 {
		t.Errorf("unknown edit = %d", rec.Code)
	}
	if rec := e.postAs(t, "a1", "/monitors/nope", url.Values{"name": {"x"}, "url": {"https://x.example"}}); rec.Code != 404 {
		t.Errorf("unknown update = %d", rec.Code)
	}
	// The parent list offers the other monitors, not the monitor itself.
	other := e.addMonitor(t, "DB", "https://db.example.com")
	edit := e.getAs(t, "a1", "GET", "/monitors/"+id+"/edit").Body.String()
	if !strings.Contains(edit, `<option value="`+other+`">DB</option>`) || strings.Contains(edit, `<option value="`+id+`"`) {
		t.Errorf("parent choices wrong:\n%s", edit)
	}
}

func TestMonitorFormParsing(t *testing.T) {
	errs := map[string]string{}
	if got := headersJSON("A: 1\n\n B :  two words \n", errs); got != `[{"name":"A","value":"1"},{"name":"B","value":"two words"}]` || len(errs) != 0 {
		t.Errorf("headers = %s %v", got, errs)
	}
	if headersJSON(": x", errs); errs["headers"] == "" {
		t.Error("a nameless header is accepted")
	}
	errs = map[string]string{}
	for in, want := range map[string]int{"": 0, "5": 5000, "0.25": 250, " 1.5 ": 1500} {
		if got := milliseconds(in, "timeout", errs); got != want {
			t.Errorf("milliseconds(%q) = %d", in, got)
		}
	}
	for _, bad := range []string{"x", "NaN", "1e9"} {
		errs = map[string]string{}
		if milliseconds(bad, "timeout", errs); errs["timeout"] == "" {
			t.Errorf("milliseconds(%q) accepted", bad)
		}
	}
	errs = map[string]string{}
	if got := tlsDaysJSON("30, 14 7", errs); got != "[30,14,7]" {
		t.Errorf("tls days = %s", got)
	}
	if tlsDaysJSON("30, soon", errs); errs["tls_warning_days"] == "" {
		t.Error("bad TLS days accepted")
	}
	got := assertionsJSON([]templates.AssertionField{
		{Path: "$.a", Op: "equals", Value: "ok"},
		{Path: "$.b", Op: "equals", Value: `"ok"`},
		{Path: "$.c", Op: "equals", Value: "true"},
		{Path: "$.d", Op: "equals", Value: "{x}"},
		{Path: "$.e", Op: "exists"},
	})
	want := `[{"path":"$.a","op":"equals","value":"ok"},{"path":"$.b","op":"equals","value":"ok"},{"path":"$.c","op":"equals","value":true},{"path":"$.d","op":"equals","value":"{x}"},{"path":"$.e","op":"exists"}]`
	if got != want {
		t.Errorf("assertions =\n%s\nwant\n%s", got, want)
	}
	if f := formFromMonitor(store.Monitor{}, store.HTTPConfig{JSONAssertions: `[{"path":"$.a","op":"exists"}]`}, nil); len(f.Assertions) != 1 {
		t.Errorf("stored assertions read as %d rows", len(f.Assertions))
	}
	if n := len(padAssertions(nil)); n != 3 {
		t.Errorf("blank rows = %d", n)
	}
	if n := len(padAssertions(make([]templates.AssertionField, 19))); n != 20 {
		t.Errorf("rows near the limit = %d", n)
	}
	// Round trip: what the store holds comes back as it was typed.
	f := formFromMonitor(store.Monitor{IntervalSeconds: 30, TimeoutMS: 500, RetryDelayMS: 5000},
		store.HTTPConfig{Headers: `[{"name":"A","value":"1"}]`, TLSWarningDays: "[30,7]", MaxBodyBytes: 1 << 20}, []string{"a", "b"})
	if f.Timeout != "0.5" || f.Headers != "A: 1" || f.TLSDays != "30, 7" || f.MaxBodyKiB != "1024" || f.Tags != "a, b" {
		t.Errorf("form = %+v", f)
	}
}
