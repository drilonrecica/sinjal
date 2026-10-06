package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/web/sse"
)

// eventStream is an open GET /events response.
type eventStream struct {
	t      *testing.T
	resp   *http.Response
	frames chan string // closed when the stream ends
}

// openEvents requests /events on a running server with a session cookie
// ("" for none) and, for a stream, consumes the opening frame.
func openEvents(t *testing.T, base, token string) *eventStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/events", nil)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: plainSessionCookie, Value: token})
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Do(r)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	s := &eventStream{t: t, resp: resp, frames: make(chan string, 64)}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		close(s.frames)
		return s
	}
	go func() {
		defer close(s.frames)
		br := bufio.NewReader(resp.Body)
		var frame strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if line == "\n" {
				s.frames <- frame.String()
				frame.Reset()
				continue
			}
			frame.WriteString(line)
		}
	}()
	if got := s.next(); !strings.HasPrefix(got, ": connected\n") {
		t.Fatalf("opening frame = %q", got)
	}
	return s
}

func (s *eventStream) next() string {
	s.t.Helper()
	select {
	case f, ok := <-s.frames:
		if !ok {
			s.t.Fatal("the event stream ended")
		}
		return f
	case <-time.After(5 * time.Second):
		s.t.Fatal("no event within 5 s")
		return ""
	}
}

func waitEventClients(t *testing.T, hub *sse.Hub, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for hub.Stats().Clients != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d event streams open, want %d", hub.Stats().Clients, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestEventsNeedASession(t *testing.T) {
	e := newAppEnv(t)
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close) // after the streams of the test have been closed

	s := openEvents(t, srv.URL, "")
	if s.resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(s.resp.Header.Get("Location"), "/login") {
		t.Fatalf("signed out: status %d, Location %q", s.resp.StatusCode, s.resp.Header.Get("Location"))
	}
	if s := openEvents(t, srv.URL, "not-a-session"); s.resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("bad cookie: status %d", s.resp.StatusCode)
	}
	if n := e.events.Stats().Clients; n != 0 {
		t.Fatalf("%d streams open without a session", n)
	}
}

// Admins and viewers both get the stream, with the security headers of
// every other response.
func TestEventsStreamForAdminAndViewer(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "u-admin", "admin", "admin", "")
	e.addUser(t, "u-viewer", "viewer", "viewer", "")
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close) // after the streams of the test have been closed

	adminToken, _ := e.signIn(t, "u-admin")
	viewerToken, _ := e.signIn(t, "u-viewer")
	admin, viewer := openEvents(t, srv.URL, adminToken), openEvents(t, srv.URL, viewerToken)
	for name, s := range map[string]*eventStream{"admin": admin, "viewer": viewer} {
		if s.resp.StatusCode != http.StatusOK || s.resp.Header.Get("Content-Type") != "text/event-stream" {
			t.Fatalf("%s: status %d, Content-Type %q", name, s.resp.StatusCode, s.resp.Header.Get("Content-Type"))
		}
		if s.resp.Header.Get("Content-Security-Policy") == "" || s.resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: headers %v", name, s.resp.Header)
		}
	}
	waitEventClients(t, e.events, 2)

	e.events.Publish(sse.MonitorUpdated, "abc123")
	want := "id: 1\nevent: monitor.updated\ndata: {\"monitor_id\":\"abc123\"}\n"
	for name, s := range map[string]*eventStream{"admin": admin, "viewer": viewer} {
		if got := s.next(); got != want {
			t.Fatalf("%s got %q, want %q", name, got, want)
		}
	}
}

// The server's read and write timeouts (30 s in production) are for
// ordinary requests. A stream must outlive both; for the write timeout
// that takes a deadline per write in the handler.
func TestEventsOutliveTheServerTimeouts(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "u-admin", "admin", "admin", "")
	token, _ := e.signIn(t, "u-admin")

	srv := httptest.NewUnstartedServer(e.h)
	srv.Config = NewServer("", e.h)
	srv.Config.ReadTimeout, srv.Config.WriteTimeout = 150*time.Millisecond, 150*time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close) // after the streams of the test have been closed

	s := openEvents(t, srv.URL, token)
	waitEventClients(t, e.events, 1)
	time.Sleep(600 * time.Millisecond) // four times either timeout
	if n := e.events.Stats().Clients; n != 1 {
		t.Fatalf("the stream was closed by a server timeout (%d open)", n)
	}
	e.events.Publish(sse.MonitorUpdated, "late")
	if got := s.next(); !strings.Contains(got, `"monitor_id":"late"`) {
		t.Fatalf("after the timeouts: %q", got)
	}
}

// What the stream asks at every keepalive: does its session still exist?
func TestEventStreamSessionCheck(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "u-admin", "admin", "admin", "")
	e.addUser(t, "u-viewer", "viewer", "viewer", "")
	logger, _ := quietLogger()

	check := func(token string) func() bool {
		return sessionAlive(e.sessions, withCookie(req(http.MethodGet, "/events", nil), token), logger)
	}

	token, sess := e.signIn(t, "u-admin")
	alive := check(token)
	if !alive() {
		t.Fatal("a live session is reported gone")
	}
	if err := e.sessions.Delete(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if alive() {
		t.Fatal("the stream would outlive a logout")
	}

	// A disabled viewer loses the stream as well.
	viewerToken, _ := e.signIn(t, "u-viewer")
	alive = check(viewerToken)
	if !alive() {
		t.Fatal("a live viewer session is reported gone")
	}
	if _, err := e.db.Writer.Exec(`UPDATE users SET disabled = 1 WHERE id = 'u-viewer'`); err != nil {
		t.Fatal(err)
	}
	if alive() {
		t.Fatal("the stream would outlive the viewer being disabled")
	}

	// A database that cannot answer is not a logout: the stream stays and
	// is asked again at the next keepalive.
	token, _ = e.signIn(t, "u-admin")
	alive = check(token)
	e.db.Close()
	if !alive() {
		t.Fatal("a database error ended the stream")
	}
}
