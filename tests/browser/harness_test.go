// Package browser holds the small browser test suite (docs/20_TESTING.md
// "UI/browser tests"). It is its own Go module so chromedp never becomes a
// dependency of the product. Each test builds the real sinjal binary once,
// boots it on a temporary data directory and drives it with a headless
// Chrome over HTTP. Assertions read DOM text, attributes and status
// labels, never screenshots.
//
// Run with `make test-browser`. Every test skips with a clear message when
// no Chrome or Chromium is installed.
package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

const (
	startTimeout = 15 * time.Second
	// stepTimeout bounds one step in the browser; a missing element fails
	// the test instead of hanging it.
	stepTimeout = 20 * time.Second

	adminLogin    = "admin"
	adminPassword = "correct horse battery"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if binPath != "" {
		os.RemoveAll(filepath.Dir(binPath))
	}
	os.Exit(code)
}

// binary builds cmd/sinjal once per run and returns its path.
func binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sinjal-browser-")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "sinjal")
		root, err := filepath.Abs("../..")
		if err != nil {
			buildErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/sinjal")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binPath
}

// chromeNames are the executables tried in order, as chromedp itself does.
var chromeNames = []string{
	"headless_shell", "headless-shell", "chromium", "chromium-browser",
	"google-chrome", "google-chrome-stable", "chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
}

// chromePath returns the first installed Chrome or Chromium, or "".
func chromePath() string {
	for _, name := range chromeNames {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// lockedBuffer collects process output safely while the process still writes.
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

type server struct {
	base string
	logs *lockedBuffer
	stop func() // ends the process; safe to call more than once
}

// start launches `sinjal serve` on a fresh data directory and a free
// loopback port, and waits until /readyz answers 200.
func start(t *testing.T) *server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return startIn(t, filepath.Join(t.TempDir(), "data"), addr)
}

// startIn launches `sinjal serve` on dataDir at addr, so a test can stop
// the server and start it again on the same data.
func startIn(t *testing.T, dataDir, addr string) *server {
	t.Helper()
	logs := &lockedBuffer{}
	cmd := exec.Command(binary(t), "serve")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"SINJAL_DATA_DIR=" + dataDir,
		"SINJAL_LISTEN=" + addr,
		"SINJAL_LOG_FORMAT=json",
	}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cmd.Process.Kill()
			<-done
		})
	}
	t.Cleanup(stop)

	s := &server{base: "http://" + addr, logs: logs, stop: stop}
	deadline := time.Now().Add(startTimeout)
	for {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("server exited during startup: %v\n%s", err, logs)
		default:
		}
		if resp, err := http.Get(s.base + "/readyz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("server not ready within %v\n%s", startTimeout, logs)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// setupPath returns the path and query of the "Initial setup: ..." link
// the server logs at startup. The link names localhost; the browser keeps
// to the address the server listens on so its cookies stay on one host.
func (s *server) setupPath(t *testing.T) string {
	t.Helper()
	for _, line := range strings.Split(s.logs.String(), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		msg, _ := rec["msg"].(string)
		if link, ok := strings.CutPrefix(msg, "Initial setup: "); ok {
			u, err := url.Parse(link)
			if err != nil {
				t.Fatalf("setup link %q: %v", link, err)
			}
			return u.RequestURI()
		}
	}
	t.Fatalf("no setup link in the log:\n%s", s.logs)
	return ""
}

// newBrowser starts a headless Chrome for one test and returns a context
// for its first tab. It skips the test when no Chrome is installed.
func newBrowser(t *testing.T) context.Context {
	t.Helper()
	path := chromePath()
	if path == "" {
		t.Skip("browser tests need Chrome or Chromium: none found on PATH (tried " + strings.Join(chromeNames, ", ") + ")")
	}
	// A desktop-sized window, so the shell shows its monitor column.
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(path), chromedp.WindowSize(1440, 900))
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(func() { cancel(); cancelAlloc() })
	// The first action starts the browser; it must not run under a
	// step timeout, which would stop the whole browser with the step.
	if err := chromedp.Do(ctx, chromedp.Navigate("about:blank")); err != nil {
		t.Fatalf("start Chrome (%s): %v", path, err)
	}
	return ctx
}

// visitor opens a tab in a separate browser context of the same Chrome:
// no cookies or storage are shared with the other tabs, like a stranger.
func visitor(t *testing.T, browser context.Context) context.Context {
	t.Helper()
	ctx, cancel := chromedp.NewContext(browser, chromedp.WithNewBrowserContext())
	t.Cleanup(cancel)
	if err := chromedp.Do(ctx, chromedp.Navigate("about:blank")); err != nil {
		t.Fatalf("open a visitor tab: %v", err)
	}
	return ctx
}

// do runs the steps under stepTimeout and fails the test on an error.
func do(t *testing.T, ctx context.Context, what string, steps ...chromedp.Action[chromedp.Void]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if err := chromedp.Do(ctx, steps...); err != nil {
		t.Fatalf("%s: %v\n%s", what, err, location(ctx))
	}
}

// get runs one reading action under stepTimeout and returns its value.
func get[T any](t *testing.T, ctx context.Context, what string, a chromedp.Action[T]) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	v, err := chromedp.Run(ctx, a)
	if err != nil {
		t.Fatalf("%s: %v\n%s", what, err, location(ctx))
	}
	return v
}

// location names the page a failed step was on, for the failure message.
func location(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	loc, err := chromedp.Run(ctx, chromedp.Location())
	if err != nil {
		return "(page unknown)"
	}
	return "on " + loc
}

// text returns the visible text of the first element matching sel.
func text(t *testing.T, ctx context.Context, sel string) string {
	t.Helper()
	return strings.TrimSpace(get(t, ctx, "read "+sel, chromedp.Text(chromedp.CSS(sel))))
}

// eval evaluates a JavaScript expression in the page.
func eval[T any](t *testing.T, ctx context.Context, expr string) T {
	t.Helper()
	return get(t, ctx, "evaluate "+expr, chromedp.Evaluate[T](expr))
}

// createAdmin completes initial setup through the setup form.
func createAdmin(t *testing.T, ctx context.Context, s *server) {
	t.Helper()
	do(t, ctx, "initial setup",
		chromedp.Navigate(s.base+s.setupPath(t)),
		chromedp.SendKeys(chromedp.CSS("#login"), adminLogin),
		chromedp.SendKeys(chromedp.CSS("#password"), adminPassword),
		chromedp.SendKeys(chromedp.CSS("#confirm"), adminPassword),
		chromedp.Submit(chromedp.CSS("#login")),
		// Setup ends on the sign-in form.
		chromedp.WaitVisible(chromedp.CSS(`form[action="/login"] #password`)),
	)
}

// signIn fills the sign-in form at /login and waits for the app shell.
func signIn(t *testing.T, ctx context.Context, s *server, password string) {
	t.Helper()
	do(t, ctx, "sign in",
		chromedp.Navigate(s.base+"/login"),
		chromedp.SendKeys(chromedp.CSS("#login"), adminLogin),
		chromedp.SendKeys(chromedp.CSS("#password"), password),
		chromedp.Submit(chromedp.CSS("#password")),
		chromedp.WaitReady(chromedp.CSS("form.signout")),
	)
}

// signedInAdmin boots a server, creates the admin and signs in: the start
// of every test past the login test.
func signedInAdmin(t *testing.T) (context.Context, *server) {
	t.Helper()
	ctx := newBrowser(t)
	s := start(t)
	createAdmin(t, ctx, s)
	signIn(t, ctx, s, adminPassword)
	return ctx, s
}

// createHTTPMonitor fills the create form with a name and URL, checking
// every interval seconds and going down after one failure, and returns the
// new monitor's id from the detail page it lands on.
func createHTTPMonitor(t *testing.T, ctx context.Context, s *server, name, target, interval string) string {
	t.Helper()
	do(t, ctx, "create monitor "+name,
		chromedp.Navigate(s.base+"/monitors/new?type=http"),
		chromedp.SendKeys(chromedp.CSS("#name"), name),
		chromedp.SendKeys(chromedp.CSS("#url"), target),
		chromedp.SetValue(chromedp.CSS("#interval"), interval),
		chromedp.SetValue(chromedp.CSS("#timeout"), "2"),
		chromedp.SetValue(chromedp.CSS("#failure_threshold"), "1"),
		chromedp.Click(chromedp.CSS(`form.monitor-form button[type="submit"]`)),
		chromedp.WaitVisible(chromedp.CSS("header.monitor-header")),
	)
	id := get(t, ctx, "read the monitor id", chromedp.AttributeValue(chromedp.CSS("header.monitor-header"), "data-monitor-id"))
	if id.Value == "" {
		t.Fatal("the detail header carries no monitor id")
	}
	return id.Value
}

// waitState waits until the element matching sel has data-state=want,
// without reloading the page.
func waitState(t *testing.T, ctx context.Context, sel, want string, within time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, within+5*time.Second)
	defer cancel()
	js := fmt.Sprintf(`(document.querySelector(%q) || {}).dataset?.state === %q`, sel, want)
	if _, err := chromedp.Run(ctx, chromedp.Poll[bool](js, chromedp.WithPollingTimeout(within), chromedp.WithPollingInterval(100*time.Millisecond))); err != nil {
		got := ""
		if v, err := chromedp.Run(context.WithoutCancel(ctx), chromedp.Evaluate[string](fmt.Sprintf(`(document.querySelector(%q) || {}).dataset?.state || ""`, sel))); err == nil {
			got = v
		}
		t.Fatalf("%s state = %q, want %q within %v: %v", sel, got, want, within, err)
	}
}
