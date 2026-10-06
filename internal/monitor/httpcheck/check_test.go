package httpcheck

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/drilonrecica/sinjal/internal/monitor"
	"github.com/drilonrecica/sinjal/internal/secret"
)

func expect(t *testing.T, expr string) monitor.StatusExpr {
	t.Helper()
	s, err := monitor.ParseStatus(expr)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func cfgFor(t *testing.T, url string) Config {
	return Config{URL: url, Method: http.MethodGet, Expected: expect(t, "200-399"), MaxBodyBytes: 1 << 20,
		FollowRedirects: true, Timeout: 2 * time.Second}
}

func TestCheckMethodsAndBody(t *testing.T) {
	var got struct{ method, body string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.body = r.Method, string(b)
		io.WriteString(w, "fine")
	}))
	defer srv.Close()
	p := NewPool("Sinjal/test")
	defer p.Close()
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		cfg := cfgFor(t, srv.URL)
		cfg.Method, cfg.Body = m, `{"ping":1}`
		res := p.Check(context.Background(), cfg)
		if !res.Success || res.StatusCode != 200 || res.Kind != "" || res.Message != "" || res.Snippet != "" {
			t.Errorf("%s: result = %+v", m, res)
		}
		if got.method != m {
			t.Errorf("server saw %s, want %s", got.method, m)
		}
		if wantBody := map[string]string{"POST": `{"ping":1}`}[m]; got.body != wantBody {
			t.Errorf("%s sent body %q, want %q", m, got.body, wantBody)
		}
	}
}

func TestCheckHeadersAuthAndUserAgent(t *testing.T) {
	var h http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h = r.Header.Clone() }))
	defer srv.Close()
	p := NewPool("Sinjal/1.2.3")
	defer p.Close()

	cfg := cfgFor(t, srv.URL)
	cfg.Headers = []monitor.Header{{Name: "Accept", Value: "application/json"}, {Name: "X-Api-Key", Value: "plain"}}
	cfg.Secrets = map[string]secret.String{"header.X-Api-Key": "s3cret-key", "auth.bearer": "tok3n"}
	if res := p.Check(context.Background(), cfg); !res.Success {
		t.Fatalf("result = %+v", res)
	}
	if h.Get("User-Agent") != "Sinjal/1.2.3" || h.Get("Accept") != "application/json" {
		t.Errorf("headers = %v", h)
	}
	if h.Get("X-Api-Key") != "s3cret-key" {
		t.Errorf("the secret header must win over a plain one: %q", h.Get("X-Api-Key"))
	}
	if h.Get("Authorization") != "Bearer tok3n" {
		t.Errorf("Authorization = %q", h.Get("Authorization"))
	}

	cfg = cfgFor(t, srv.URL)
	cfg.UserAgent = "probe/9"
	cfg.Secrets = map[string]secret.String{"auth.basic": "alice:pa:ss"}
	p.Check(context.Background(), cfg)
	if h.Get("User-Agent") != "probe/9" {
		t.Errorf("custom User-Agent = %q", h.Get("User-Agent"))
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:pa:ss")); h.Get("Authorization") != want {
		t.Errorf("Authorization = %q, want %q", h.Get("Authorization"), want)
	}
}

func TestCheckStatusFailureSnippet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, "maintenance in progress")
	}))
	defer srv.Close()
	p := NewPool("Sinjal/test")
	defer p.Close()
	res := p.Check(context.Background(), cfgFor(t, srv.URL))
	if res.Success || res.Kind != KindHTTPStatus || res.StatusCode != 503 {
		t.Fatalf("result = %+v", res)
	}
	if res.Message != "status 503, expected 200-399" || res.Snippet != "maintenance in progress" {
		t.Errorf("message = %q, snippet = %q", res.Message, res.Snippet)
	}
	if res.Duration <= 0 || !res.Finished.After(res.Started) {
		t.Errorf("timing = %v, %v -> %v", res.Duration, res.Started, res.Finished)
	}

	cfg := cfgFor(t, srv.URL)
	cfg.Expected = expect(t, "503")
	if res := p.Check(context.Background(), cfg); !res.Success || res.Snippet != "" {
		t.Errorf("an expected 503 is a success without a snippet: %+v", res)
	}
}

func TestCheckScrubsSecrets(t *testing.T) {
	const basic = "alice:hunter2-pass"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		// Echo everything a careless server might reflect.
		io.WriteString(w, strings.Repeat("x", SnippetMax-12)+r.Header.Get("X-Api-Key")+"|")
		io.WriteString(w, r.Header.Get("Authorization")+"|hunter2-pass|"+basic)
	}))
	defer srv.Close()
	p := NewPool("Sinjal/test")
	defer p.Close()
	cfg := cfgFor(t, srv.URL)
	cfg.Secrets = map[string]secret.String{"header.X-Api-Key": "api-key-0123456789", "auth.basic": basic}
	res := p.Check(context.Background(), cfg)
	if res.Kind != KindHTTPStatus {
		t.Fatalf("result = %+v", res)
	}
	// The API key straddles the 4 KiB cut: not even a prefix may survive.
	for _, leak := range []string{"api-key", "hunter2", base64.StdEncoding.EncodeToString([]byte(basic))[:8]} {
		if strings.Contains(res.Snippet, leak) {
			t.Errorf("snippet leaks %q", leak)
		}
	}
	if len(res.Snippet) > SnippetMax || !strings.Contains(res.Snippet, "[REDACTED]") {
		t.Errorf("snippet (%d bytes) ends %q", len(res.Snippet), res.Snippet[len(res.Snippet)-20:])
	}
}

func TestCheckSnippetIsValidUTF8AndCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("bad \xff\xfe bytes "))
		w.Write(bytes.Repeat([]byte("é"), 4*SnippetMax)) // 2-byte runes cut mid-rune at the cap
	}))
	defer srv.Close()
	p := NewPool("Sinjal/test")
	defer p.Close()
	res := p.Check(context.Background(), cfgFor(t, srv.URL))
	if !utf8.ValidString(res.Snippet) || len(res.Snippet) > SnippetMax || !strings.HasPrefix(res.Snippet, "bad �") {
		t.Errorf("snippet: valid=%v len=%d prefix=%q", utf8.ValidString(res.Snippet), len(res.Snippet), res.Snippet[:12])
	}
}

func TestReadCapped(t *testing.T) {
	big := bytes.Repeat([]byte("a"), 2<<20)
	b, truncated, err := readCapped(bytes.NewReader(big), 1<<20)
	if err != nil || len(b) != 1<<20 || !truncated {
		t.Errorf("2 MiB at a 1 MiB cap: len=%d truncated=%v err=%v", len(b), truncated, err)
	}
	b, truncated, _ = readCapped(bytes.NewReader(big[:1<<20]), 1<<20)
	if len(b) != 1<<20 || truncated {
		t.Errorf("exactly the cap: len=%d truncated=%v", len(b), truncated)
	}
}

func TestCheckTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	p := NewPool("Sinjal/test")
	defer p.Close()
	cfg := cfgFor(t, srv.URL)
	cfg.Timeout = 100 * time.Millisecond
	res := p.Check(context.Background(), cfg)
	if res.Kind != KindTimeout || res.Message != "no response within 100ms" {
		t.Errorf("result = %+v", res)
	}
	if res.Duration > time.Second {
		t.Errorf("the deadline must cancel the request: took %v", res.Duration)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := p.Check(ctx, cfgFor(t, srv.URL)); res.Kind != KindUnknown || res.Success {
		t.Errorf("a cancelled check = %+v, want unknown", res)
	}
}

func TestCheckFailureKinds(t *testing.T) {
	p := NewPool("Sinjal/test")
	defer p.Close()

	// Connection refused: a port that was just closed.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := "http://" + ln.Addr().String()
	ln.Close()

	// A server that answers with garbage instead of HTTP.
	garbage, _ := net.Listen("tcp", "127.0.0.1:0")
	defer garbage.Close()
	go func() {
		for {
			c, err := garbage.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("HELLO THERE\r\n\r\n"))
			c.Close()
		}
	}()

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tlsSrv.Close()
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer loop.Close()

	for name, c := range map[string]struct{ url, kind string }{
		"dns":      {"http://sinjal-no-such-host.invalid", KindDNS},
		"connect":  {closed, KindConnect},
		"tls":      {tlsSrv.URL, KindTLS},
		"protocol": {"http://" + garbage.Addr().String(), KindProtocol},
		"redirect": {loop.URL, KindProtocol},
	} {
		res := p.Check(context.Background(), cfgFor(t, c.url))
		if res.Success || res.Kind != c.kind || res.Message == "" {
			t.Errorf("%s: result = %+v, want kind %s", name, res, c.kind)
		}
	}

	cfg := cfgFor(t, tlsSrv.URL)
	cfg.Insecure = true
	if res := p.Check(context.Background(), cfg); !res.Success {
		t.Errorf("insecure TLS must accept the self-signed certificate: %+v", res)
	}
}

func TestCheckScrubsMessage(t *testing.T) {
	p := NewPool("Sinjal/test")
	defer p.Close()
	cfg := cfgFor(t, "http://tok3n-host.invalid")
	cfg.Secrets = map[string]secret.String{"auth.bearer": "tok3n"}
	if res := p.Check(context.Background(), cfg); strings.Contains(res.Message, "tok3n") {
		t.Errorf("message leaks the secret: %q", res.Message)
	}
}

func BenchmarkCheck(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	p := NewPool("Sinjal/bench")
	defer p.Close()
	s, _ := monitor.ParseStatus("200-399")
	cfg := Config{URL: srv.URL, Method: http.MethodGet, Expected: s, Timeout: 5 * time.Second,
		Headers: []monitor.Header{{Name: "Accept", Value: "*/*"}}, Secrets: map[string]secret.String{"auth.bearer": "t"}}
	b.ReportAllocs()
	for b.Loop() {
		if res := p.Check(context.Background(), cfg); !res.Success {
			b.Fatal(res)
		}
	}
}

func serve(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	t.Cleanup(srv.Close)
	return srv
}

func jsonA(path, op, val string) monitor.JSONAssertion {
	a := monitor.JSONAssertion{Path: path, Op: op}
	if val != "" {
		a.Value = []byte(val)
	}
	return a
}

func TestCheckTextAssertions(t *testing.T) {
	srv := serve(t, "Hello World, all systems go")
	p := NewPool("Sinjal/test")
	defer p.Close()
	cases := []struct {
		name, contains, notContains, kind string
	}{
		{"contains hit", "all systems", "", ""},
		{"case sensitive", "hello world", "", KindBodyAssertion},
		{"contains miss", "outage", "", KindBodyAssertion},
		{"not contains ok", "", "outage", ""},
		{"not contains hit", "", "World", KindBodyAssertion},
		{"not contains case sensitive", "", "WORLD", ""},
		{"both", "Hello", "outage", ""},
	}
	for _, c := range cases {
		cfg := cfgFor(t, srv.URL)
		cfg.BodyContains, cfg.BodyNotContains = c.contains, c.notContains
		res := p.Check(context.Background(), cfg)
		if res.Kind != c.kind || res.Success != (c.kind == "") {
			t.Errorf("%s: %+v", c.name, res)
		}
		if c.kind == "" && (res.Snippet != "" || res.Message != "") {
			t.Errorf("%s: success kept data: %+v", c.name, res)
		}
		if c.kind != "" && res.Snippet != "Hello World, all systems go" {
			t.Errorf("%s: snippet = %q", c.name, res.Snippet)
		}
	}
}

func TestCheckJSONAssertions(t *testing.T) {
	p := NewPool("Sinjal/test")
	defer p.Close()
	srv := serve(t, `{"status":"ok","n":3,"items":[{"up":true}]}`)
	cases := []struct {
		name string
		as   []monitor.JSONAssertion
		kind string
		snip string
	}{
		{"all pass", []monitor.JSONAssertion{jsonA("$.status", "equals", `"ok"`), jsonA("$.items[0].up", "equals", `true`), jsonA("$.n", "equals", `3.0`), jsonA("$.x", "not_exists", ""), jsonA("$.n", "exists", "")}, "", ""},
		{"type mismatch", []monitor.JSONAssertion{jsonA("$.n", "equals", `"3"`)}, KindJSONAssertion, `$.n equals "3"; actual: 3`},
		{"second fails", []monitor.JSONAssertion{jsonA("$.n", "exists", ""), jsonA("$.status", "not_equals", `"ok"`)}, KindJSONAssertion, `$.status not_equals "ok"; actual: "ok"`},
		{"missing", []monitor.JSONAssertion{jsonA("$.gone", "exists", "")}, KindJSONAssertion, `$.gone exists; actual: (missing)`},
	}
	for _, c := range cases {
		cfg := cfgFor(t, srv.URL)
		cfg.JSONAssertions = c.as
		res := p.Check(context.Background(), cfg)
		if res.Kind != c.kind || res.Success != (c.kind == "") || res.Snippet != c.snip {
			t.Errorf("%s: %+v", c.name, res)
		}
	}
}

func TestCheckJSONParseFailures(t *testing.T) {
	p := NewPool("Sinjal/test")
	defer p.Close()
	for name, tc := range map[string]struct {
		body string
		max  int64
	}{
		"not json":                {"<html>nope</html>", 1 << 20},
		"trailing":                {`{"a":1} junk`, 1 << 20},
		"valid prefix cut at cap": {`{"a":1}` + strings.Repeat(" ", 100), 20},
		"truncated":               {`{"a":"` + strings.Repeat("x", 100) + `"}`, 50},
	} {
		srv := serve(t, tc.body)
		cfg := cfgFor(t, srv.URL)
		cfg.MaxBodyBytes = tc.max
		cfg.JSONAssertions = []monitor.JSONAssertion{jsonA("$.a", "exists", "")}
		res := p.Check(context.Background(), cfg)
		if res.Success || res.Kind != KindJSONParse || res.Snippet == "" {
			t.Errorf("%s: %+v", name, res)
		}
	}
}

func TestCheckJSONActualTruncatedAndScrubbed(t *testing.T) {
	p := NewPool("Sinjal/test")
	defer p.Close()
	srv := serve(t, `{"v":"`+strings.Repeat("a", 500)+`","k":"hunter2"}`)
	cfg := cfgFor(t, srv.URL)
	cfg.JSONAssertions = []monitor.JSONAssertion{jsonA("$.v", "equals", `"b"`)}
	res := p.Check(context.Background(), cfg)
	if res.Kind != KindJSONAssertion || len(res.Snippet) > 300 {
		t.Fatalf("%d bytes: %+v", len(res.Snippet), res)
	}
	cfg.JSONAssertions = []monitor.JSONAssertion{jsonA("$.k", "equals", `"x"`)}
	cfg.Secrets = map[string]secret.String{monitor.SecretBearerToken: secret.String("hunter2")}
	res = p.Check(context.Background(), cfg)
	if strings.Contains(res.Snippet+res.Message, "hunter2") || !strings.Contains(res.Snippet, "[REDACTED]") {
		t.Errorf("not scrubbed: %+v", res)
	}
}

func TestCheckBodyAssertionScrubsSecretsAndStatusFirst(t *testing.T) {
	p := NewPool("Sinjal/test")
	defer p.Close()
	srv := serve(t, "token=hunter2 oops")
	cfg := cfgFor(t, srv.URL)
	cfg.BodyContains = "hunter2-missing"
	cfg.Secrets = map[string]secret.String{monitor.SecretBearerToken: secret.String("hunter2")}
	res := p.Check(context.Background(), cfg)
	if res.Kind != KindBodyAssertion || strings.Contains(res.Snippet, "hunter2") {
		t.Errorf("%+v", res)
	}
	cfg.Expected = expect(t, "500")
	cfg.BodyContains = "token"
	if res = p.Check(context.Background(), cfg); res.Kind != KindHTTPStatus {
		t.Errorf("status should fail first: %+v", res)
	}
}

func TestCheckAssertionsRespectReadCap(t *testing.T) {
	p := NewPool("Sinjal/test")
	defer p.Close()
	srv := serve(t, strings.Repeat("a", 100)+"needle")
	cfg := cfgFor(t, srv.URL)
	cfg.MaxBodyBytes = 50
	cfg.BodyContains = "needle"
	if res := p.Check(context.Background(), cfg); res.Kind != KindBodyAssertion {
		t.Errorf("needle past the cap must not match: %+v", res)
	}
}
