package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/assets"
)

// lockedBuffer is a log sink that is safe to read while the server goroutine
// is still writing access-log lines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func quietLogger() (*slog.Logger, *lockedBuffer) {
	buf := &lockedBuffer{}
	return slog.New(slog.NewTextHandler(buf, nil)), buf
}

func TestNewRouterInstallsMiddleware(t *testing.T) {
	logger, buf := quietLogger()
	r := NewRouter(logger, nil)
	r.Get("/s/{token}", func(w http.ResponseWriter, _ *http.Request) { panic("x") })

	srv := newTestServer(t, r)
	resp, err := http.Get(srv + "/s/SECRET")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 500 || resp.Header.Get("X-Request-Id") == "" {
		t.Errorf("status = %d, request id = %q", resp.StatusCode, resp.Header.Get("X-Request-Id"))
	}
	out := buf.String()
	if !strings.Contains(out, "subsystem=http") || strings.Contains(out, "SECRET") {
		t.Errorf("log should carry subsystem=http and no token: %s", out)
	}
}

func TestNewRouterResolvesTrustedProxy(t *testing.T) {
	for _, tc := range []struct {
		trusted []netip.Prefix
		want    string
	}{
		{nil, "client_ip=127.0.0.1"},
		{[]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, "client_ip=203.0.113.9"},
	} {
		logger, buf := quietLogger()
		r := NewRouter(logger, tc.trusted)
		r.Get("/x", func(w http.ResponseWriter, _ *http.Request) {})
		srv := newTestServer(t, r)
		req, _ := http.NewRequest("GET", srv+"/x", nil)
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if !strings.Contains(buf.String(), tc.want) {
			t.Errorf("trusted=%v: access log %q lacks %q", tc.trusted, buf.String(), tc.want)
		}
	}
}

func TestNewServerTimeouts(t *testing.T) {
	srv := NewServer(":8080", http.NotFoundHandler())
	if srv.Addr != ":8080" {
		t.Errorf("Addr = %q", srv.Addr)
	}
	for name, d := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout,
		"ReadTimeout":       srv.ReadTimeout,
		"WriteTimeout":      srv.WriteTimeout,
		"IdleTimeout":       srv.IdleTimeout,
	} {
		if d <= 0 {
			t.Errorf("%s is not set", name)
		}
	}
}

// newTestServer runs Run on a loopback listener until the test ends.
func newTestServer(t *testing.T, h http.Handler) string {
	t.Helper()
	addr, stop := startRun(t, h, 5*time.Second)
	t.Cleanup(func() { stop() })
	return addr
}

// startRun starts Run and returns the base URL and a stop func that cancels
// the context and returns Run's result and how long shutdown took.
func startRun(t *testing.T, h http.Handler, grace time.Duration) (string, func() (time.Duration, error)) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logger, _ := quietLogger()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- Run(ctx, NewServer(ln.Addr().String(), h), ln, grace, logger) }()

	var once bool
	var err2 error
	var took time.Duration
	stop := func() (time.Duration, error) {
		if !once {
			once = true
			start := time.Now()
			cancel()
			select {
			case err2 = <-result:
			case <-time.After(30 * time.Second):
				t.Error("Run did not return")
			}
			took = time.Since(start)
		}
		return took, err2
	}
	return "http://" + ln.Addr().String(), stop
}

func TestRunServesAndStopsOnCancel(t *testing.T) {
	addr, stop := startRun(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "pong")
	}), 5*time.Second)

	resp, err := http.Get(addr)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "pong" {
		t.Errorf("body = %q", body)
	}
	if _, err := stop(); err != nil {
		t.Errorf("Run returned %v", err)
	}
	if _, err := http.Get(addr); err == nil {
		t.Error("server still accepting connections after shutdown")
	}
}

func TestShutdownLetsInFlightRequestFinish(t *testing.T) {
	started := make(chan struct{})
	addr, stop := startRun(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(300 * time.Millisecond)
		io.WriteString(w, "finished")
	}), 5*time.Second)

	type result struct {
		body string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get(addr)
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		got <- result{body: string(b), err: err}
	}()

	<-started
	if _, err := stop(); err != nil {
		t.Errorf("Run returned %v", err)
	}
	r := <-got
	if r.err != nil || r.body != "finished" {
		t.Errorf("in-flight request was cut off: body=%q err=%v", r.body, r.err)
	}
}

func TestShutdownIsBoundedByGrace(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	addr, stop := startRun(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release // a request that never finishes on its own
	}), 150*time.Millisecond)

	clientErr := make(chan error, 1)
	go func() {
		resp, err := http.Get(addr)
		if err == nil {
			resp.Body.Close()
		}
		clientErr <- err
	}()

	<-started
	took, err := stop()
	if err != nil {
		t.Errorf("a forced close after the grace period is not an error, got %v", err)
	}
	if took > 5*time.Second {
		t.Errorf("shutdown took %v, want it bounded by the 150ms grace", took)
	}
	if took < 100*time.Millisecond {
		t.Errorf("shutdown took %v; it should have waited for the grace period", took)
	}
	if err := <-clientErr; err == nil {
		t.Error("the hung request should have been cut off")
	}
}

func TestRunReportsServeFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close() // Serve on a closed listener fails immediately
	logger, _ := quietLogger()
	err = Run(context.Background(), NewServer("", http.NotFoundHandler()), ln, time.Second, logger)
	if err == nil {
		t.Error("expected Run to report the serve error")
	}
}

func TestStaticRoute(t *testing.T) {
	logger, buf := quietLogger()
	r := NewRouter(logger, nil)
	RegisterStatic(r, assets.Default)

	srv := newTestServer(t, r)
	url := assets.URL("js/htmx.min.js")
	resp, err := http.Get(srv + url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("GET %s = %d, Cache-Control %q", url, resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	if !strings.Contains(buf.String(), "route=/static/*") {
		t.Errorf("access log should show the route pattern, got: %s", buf.String())
	}

	resp, err = http.Get(srv + "/static/js/htmx.min.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unhashed URL = %d, want 404", resp.StatusCode)
	}
}
