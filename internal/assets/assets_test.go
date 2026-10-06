package assets

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"css/base.css":   {Data: []byte(strings.Repeat("body { color: red; }\n", 50))},
		"js/app.js":      {Data: []byte(strings.Repeat("console.log('hi');\n", 50))},
		"fonts/ui.woff2": {Data: []byte("not really a font, not compressible text")},
		"tiny.txt":       {Data: []byte("x")},
	}
}

func mustNew(t *testing.T, fsys fstest.MapFS) *Registry {
	t.Helper()
	r, err := New(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func do(h http.Handler, method, url string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestURLContainsContentHash(t *testing.T) {
	r := mustNew(t, testFS())
	u := r.URL("css/base.css")
	if !regexp.MustCompile(`^/static/css/base\.[0-9a-f]{16}\.css$`).MatchString(u) {
		t.Errorf("URL = %q", u)
	}

	changed := testFS()
	changed["css/base.css"] = &fstest.MapFile{Data: []byte("body { color: blue; }")}
	if u2 := mustNew(t, changed).URL("css/base.css"); u2 == u {
		t.Error("URL did not change when the content changed")
	}
	if u3 := mustNew(t, testFS()).URL("css/base.css"); u3 != u {
		t.Error("URL is not stable for identical content")
	}
}

func TestURLUnknownPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected a panic for an unknown asset")
		}
	}()
	mustNew(t, testFS()).URL("css/missing.css")
}

func TestServeIdentity(t *testing.T) {
	r := mustNew(t, testFS())
	rec := do(r.Handler(), "GET", r.URL("css/base.css"), nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	h := rec.Header()
	if h.Get("Content-Type") != "text/css; charset=utf-8" {
		t.Errorf("Content-Type = %q", h.Get("Content-Type"))
	}
	if cc := h.Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("Cache-Control = %q", cc)
	}
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("ETag") == "" {
		t.Errorf("headers = %v", h)
	}
	if h.Get("Content-Encoding") != "" {
		t.Error("identity request got a Content-Encoding")
	}
	if !strings.Contains(h.Get("Vary"), "Accept-Encoding") {
		t.Errorf("Vary = %q", h.Get("Vary"))
	}
	if rec.Body.String() != string(testFS()["css/base.css"].Data) {
		t.Error("body differs from the source file")
	}
}

func TestServeGzip(t *testing.T) {
	r := mustNew(t, testFS())
	url := r.URL("css/base.css")
	plain := do(r.Handler(), "GET", url, nil)
	rec := do(r.Handler(), "GET", url, map[string]string{"Accept-Encoding": "br, gzip;q=0.8"})

	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", rec.Header().Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != string(testFS()["css/base.css"].Data) {
		t.Error("decompressed body differs from the source file")
	}
	if rec.Header().Get("ETag") == plain.Header().Get("ETag") {
		t.Error("gzip and identity representations must have distinct ETags")
	}
	if rec.Body.Len() >= len(testFS()["css/base.css"].Data) {
		t.Error("gzip body is not smaller")
	}
}

func TestGzipRefusedWhenQZero(t *testing.T) {
	r := mustNew(t, testFS())
	rec := do(r.Handler(), "GET", r.URL("css/base.css"), map[string]string{"Accept-Encoding": "gzip;q=0"})
	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("gzip;q=0 must not be served gzip")
	}
}

func TestBinaryAndTinyFilesNotGzipped(t *testing.T) {
	r := mustNew(t, testFS())
	for _, name := range []string{"fonts/ui.woff2", "tiny.txt"} {
		rec := do(r.Handler(), "GET", r.URL(name), map[string]string{"Accept-Encoding": "gzip"})
		if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s: status %d, encoding %q", name, rec.Code, rec.Header().Get("Content-Encoding"))
		}
	}
	rec := do(r.Handler(), "GET", r.URL("fonts/ui.woff2"), nil)
	if rec.Header().Get("Content-Type") != "font/woff2" {
		t.Errorf("woff2 Content-Type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestConditionalRequest(t *testing.T) {
	r := mustNew(t, testFS())
	url := r.URL("js/app.js")
	etag := do(r.Handler(), "GET", url, nil).Header().Get("ETag")

	rec := do(r.Handler(), "GET", url, map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("status = %d, body %d bytes; want 304 empty", rec.Code, rec.Body.Len())
	}
	rec = do(r.Handler(), "GET", url, map[string]string{"If-None-Match": `"other"`})
	if rec.Code != 200 {
		t.Errorf("non-matching ETag: status = %d", rec.Code)
	}
}

func TestHEAD(t *testing.T) {
	r := mustNew(t, testFS())
	rec := do(r.Handler(), "HEAD", r.URL("css/base.css"), nil)
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD: status %d, body %d, Content-Length %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
	}
}

func TestUnknownAndUnhashedPathsAre404(t *testing.T) {
	r := mustNew(t, testFS())
	for _, p := range []string{
		"/static/css/base.css", // logical name, not the hashed one
		"/static/css/base.0000000000000000.css",
		"/static/",
		"/static/../etc/passwd",
		"/static/css/../css/" + strings.TrimPrefix(r.URL("css/base.css"), "/static/css/"),
		"/static/%2e%2e/secret",
	} {
		if rec := do(r.Handler(), "GET", p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
}

func TestUnsupportedFileTypeRejected(t *testing.T) {
	_, err := New(fstest.MapFS{"notes.exe": {Data: []byte("x")}})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("err = %v, want an unsupported-type error", err)
	}
}

func TestEmbeddedAssets(t *testing.T) {
	url := URL("js/htmx.min.js")
	rec := do(Default.Handler(), "GET", url, nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"htmx 2.0.11", "https://cdn.jsdelivr.net/npm/htmx.org@2.0.11", "0BSD", "upstream sha256:"} {
		if !strings.Contains(body[:600], want) {
			t.Errorf("htmx header is missing %q", want)
		}
	}
	if !strings.Contains(body, "var htmx=") {
		t.Error("vendored htmx body missing")
	}
	if rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Errorf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
}
