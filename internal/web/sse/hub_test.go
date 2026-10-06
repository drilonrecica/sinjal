package sse

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func quietHub() *Hub { return NewHub(slog.New(slog.NewTextHandler(io.Discard, nil))) }

// serveHub runs the hub's handler on a real server.
func serveHub(t *testing.T, h *Hub) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.Serve(w, r, nil) }))
	t.Cleanup(srv.Close)
	return srv
}

// stream is one connected client reading frames.
type stream struct {
	t      *testing.T
	resp   *http.Response
	frames chan string // one per frame, without the blank line; closed at end of stream
	err    chan error  // how the stream ended
	cancel context.CancelFunc
}

// connect opens a stream and consumes the opening frame.
func connect(t *testing.T, url string) *stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	s := &stream{t: t, resp: resp, frames: make(chan string, 4096), err: make(chan error, 1), cancel: cancel}
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		return s
	}
	go func() {
		defer close(s.frames)
		r := bufio.NewReader(resp.Body)
		var frame strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				s.err <- err
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
	if got := s.next(); got != ": connected\nretry: 3000\n" {
		t.Fatalf("opening frame = %q", got)
	}
	return s
}

// next returns the next frame, failing the test if none arrives.
func (s *stream) next() string {
	s.t.Helper()
	select {
	case f, ok := <-s.frames:
		if !ok {
			s.t.Fatal("the stream ended")
		}
		return f
	case <-time.After(5 * time.Second):
		s.t.Fatal("no frame within 5 s")
		return ""
	}
}

// ended waits for the end of the stream and returns the read error.
func (s *stream) ended() error {
	s.t.Helper()
	select {
	case err := <-s.err:
		return err
	case <-time.After(5 * time.Second):
		s.t.Fatal("the stream did not end within 5 s")
		return nil
	}
}

func waitClients(t *testing.T, h *Hub, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.Stats().Clients != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d clients connected, want %d", h.Stats().Clients, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStreamDeliversEventsInOrder(t *testing.T) {
	h := quietHub()
	srv := serveHub(t, h)
	a, b := connect(t, srv.URL), connect(t, srv.URL)
	waitClients(t, h, 2)

	if ct := a.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := a.resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if xa := a.resp.Header.Get("X-Accel-Buffering"); xa != "no" {
		t.Errorf("X-Accel-Buffering = %q", xa)
	}

	h.Publish(MonitorCreated, "m1")
	h.Publish(MonitorUpdated, "m1")
	h.Publish(MonitorDeleted, "m2")
	want := []string{
		"id: 1\nevent: monitor.created\ndata: {\"monitor_id\":\"m1\"}\n",
		"id: 2\nevent: monitor.updated\ndata: {\"monitor_id\":\"m1\"}\n",
		"id: 3\nevent: monitor.deleted\ndata: {\"monitor_id\":\"m2\"}\n",
	}
	for _, s := range []*stream{a, b} {
		for i, w := range want {
			if got := s.next(); got != w {
				t.Fatalf("frame %d = %q, want %q", i+1, got, w)
			}
		}
	}
}

// Ids keep counting while nobody listens, so a client can tell from a gap
// that it missed events.
func TestEventIDsCountEveryEvent(t *testing.T) {
	h := quietHub()
	srv := serveHub(t, h)
	h.Publish(MonitorUpdated, "m1")
	h.Publish(MonitorUpdated, "m1")
	s := connect(t, srv.URL)
	waitClients(t, h, 1)
	h.Publish(MonitorUpdated, "m1")
	if got := s.next(); !strings.HasPrefix(got, "id: 3\n") {
		t.Fatalf("frame = %q, want id 3", got)
	}
}

// A payload is one line of JSON whatever the id contains: nothing a caller
// passes can start a second frame.
func TestFrameEscapesTheMonitorID(t *testing.T) {
	got := string(frame(7, MonitorUpdated, "a\"b\n\nevent: x"))
	want := "id: 7\nevent: monitor.updated\ndata: {\"monitor_id\":\"a\\\"b\\n\\nevent: x\"}\n\n"
	if got != want {
		t.Fatalf("frame = %q, want %q", got, want)
	}
}

// A client that does not keep up loses its stream; the others and the
// publisher are not affected.
func TestSlowClientIsDropped(t *testing.T) {
	h := quietHub()
	srv := serveHub(t, h)
	healthy := connect(t, srv.URL)
	waitClients(t, h, 1)
	stuck := h.subscribe() // never read: a handler blocked on a stalled connection

	// More than the stuck client's buffer, in chunks the healthy client
	// reads completely.
	total := 0
	for total < clientBuffer+50 {
		for range 50 {
			total++
			h.Publish(MonitorUpdated, fmt.Sprint(total))
		}
		for i := total - 49; i <= total; i++ {
			if got, want := healthy.next(), fmt.Sprintf("id: %d\nevent: monitor.updated\ndata: {\"monitor_id\":\"%d\"}\n", i, i); got != want {
				t.Fatalf("healthy client: frame %q, want %q", got, want)
			}
		}
	}

	if st := h.Stats(); st.Clients != 1 || st.Dropped != 1 {
		t.Fatalf("stats = %+v, want the stuck client dropped and the healthy one kept", st)
	}
	// The stuck client's channel holds what fitted and is then closed.
	n := 0
	for range stuck.frames {
		n++
	}
	if n != clientBuffer {
		t.Fatalf("the stuck client had %d frames buffered, want %d", n, clientBuffer)
	}
}

func TestPublishNeverBlocks(t *testing.T) {
	h := quietHub()
	h.subscribe() // never read
	done := make(chan struct{})
	go func() {
		for range 10000 {
			h.Publish(MonitorUpdated, "m1")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a client that does not read")
	}
	if st := h.Stats(); st.Clients != 0 || st.Dropped != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestCloseEndsStreamsAndRefusesNewOnes(t *testing.T) {
	h := quietHub()
	srv := serveHub(t, h)
	s := connect(t, srv.URL)
	waitClients(t, h, 1)
	h.Publish(MonitorUpdated, "m1")

	h.Close()
	// What was published before the close still arrives, then the stream
	// ends cleanly, not with a broken connection.
	if got := s.next(); !strings.HasPrefix(got, "id: 1\n") {
		t.Fatalf("frame = %q", got)
	}
	if err := s.ended(); err != io.EOF {
		t.Fatalf("the stream ended with %v, want a clean EOF", err)
	}
	if st := h.Stats(); st.Clients != 0 || st.Dropped != 0 {
		t.Fatalf("stats after Close = %+v", st)
	}

	late := connect(t, srv.URL)
	if late.resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a stream opened after Close: status %d", late.resp.StatusCode)
	}
	h.Publish(MonitorUpdated, "m1") // must not panic
	h.Close()                       // nor a second Close
}

func TestDisconnectedClientIsForgotten(t *testing.T) {
	h := quietHub()
	srv := serveHub(t, h)
	s := connect(t, srv.URL)
	waitClients(t, h, 1)
	s.cancel()
	waitClients(t, h, 0)
	if st := h.Stats(); st.Dropped != 0 {
		t.Fatalf("a client that left counts as dropped: %+v", st)
	}
}

// fakeWriter is a ResponseWriter for the virtual clock: it records what
// the handler writes and the write deadline it sets.
type fakeWriter struct {
	mu             sync.Mutex
	header         http.Header
	status         int
	body           bytes.Buffer
	flushed        int // bytes of body that have been flushed
	writeDeadline  time.Time
	writtenWithout bool // a write happened later than the deadline in force
	writes         int
	fail           error
	gate           chan struct{} // when set, every write waits for a value
}

func (f *fakeWriter) Header() http.Header { return f.header }

func (f *fakeWriter) WriteHeader(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = code
}

func (f *fakeWriter) Write(b []byte) (int, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if f.fail != nil {
		return 0, f.fail
	}
	if !time.Now().Before(f.writeDeadline) {
		f.writtenWithout = true
	}
	return f.body.Write(b)
}

func (f *fakeWriter) Flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushed = f.body.Len()
}

func (f *fakeWriter) SetWriteDeadline(t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeDeadline = t
	return nil
}

// sent returns what has been flushed to the client since the last call.
func (f *fakeWriter) sent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := string(f.body.Next(f.flushed))
	f.flushed = 0
	return out
}

func TestKeepaliveAndSessionCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := quietHub()
		w := &fakeWriter{header: http.Header{}}
		var mu sync.Mutex
		alive, checks := true, 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.Serve(w, httptest.NewRequest(http.MethodGet, "/events", nil), func() bool {
				mu.Lock()
				defer mu.Unlock()
				checks++
				return alive
			})
		}()
		synctest.Wait()
		if got := w.sent(); got != ": connected\nretry: 3000\n\n" || w.status != http.StatusOK {
			t.Fatalf("opening: status %d, %q", w.status, got)
		}
		// Nothing is written while nothing happens, until the keepalive.
		time.Sleep(keepaliveEvery - time.Second)
		synctest.Wait()
		if got := w.sent(); got != "" {
			t.Fatalf("written before the keepalive was due: %q", got)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := w.sent(); got != ": keepalive\n\n" {
			t.Fatalf("keepalive = %q", got)
		}
		// Every write gets a fresh deadline of its own.
		if want := time.Now().Add(writeTimeout); !w.writeDeadline.Equal(want) || w.writtenWithout {
			t.Fatalf("write deadline %v, want %v (written past a deadline: %v)", w.writeDeadline, want, w.writtenWithout)
		}

		// An event between keepalives is written at once.
		time.Sleep(5 * time.Second)
		h.Publish(MonitorUpdated, "m1")
		synctest.Wait()
		if got := w.sent(); got != "id: 1\nevent: monitor.updated\ndata: {\"monitor_id\":\"m1\"}\n\n" {
			t.Fatalf("event = %q", got)
		}

		// The session is gone: the next keepalive ends the stream instead.
		mu.Lock()
		alive = false
		mu.Unlock()
		time.Sleep(keepaliveEvery)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("the stream outlived its session")
		}
		if got := w.sent(); got != "" {
			t.Fatalf("written after the session ended: %q", got)
		}
		if checks != 2 || h.Stats().Clients != 0 {
			t.Fatalf("%d session checks, %d clients", checks, h.Stats().Clients)
		}
	})
}

// Events that pile up while a write is in progress go out together in the
// next one: a burst costs a slow connection one write, not one per event.
func TestWaitingEventsAreWrittenTogether(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := quietHub()
		w := &fakeWriter{header: http.Header{}, gate: make(chan struct{})}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.Serve(w, httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx), nil)
		}()
		w.gate <- struct{}{} // the opening
		synctest.Wait()
		w.sent()

		h.Publish(MonitorUpdated, "first")
		synctest.Wait() // the handler is now inside the write of "first"
		for i := range 3 {
			h.Publish(MonitorUpdated, fmt.Sprint("m", i))
		}
		w.gate <- struct{}{}
		synctest.Wait()
		if got := w.sent(); !strings.Contains(got, `"first"`) || strings.Contains(got, `"m0"`) {
			t.Fatalf("first write = %q", got)
		}
		before := w.writes
		w.gate <- struct{}{}
		synctest.Wait()
		want := "id: 2\nevent: monitor.updated\ndata: {\"monitor_id\":\"m0\"}\n\n" +
			"id: 3\nevent: monitor.updated\ndata: {\"monitor_id\":\"m1\"}\n\n" +
			"id: 4\nevent: monitor.updated\ndata: {\"monitor_id\":\"m2\"}\n\n"
		if got := w.sent(); got != want || w.writes != before+1 {
			t.Fatalf("%d writes carried %q, want one write of %q", w.writes-before, got, want)
		}

		// The request ending (client gone) ends the handler.
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("the handler outlived its request")
		}
		if h.Stats().Clients != 0 {
			t.Fatal("the client is still subscribed")
		}
	})
}

// A write that fails (the client stalled past the write deadline, or the
// connection broke) ends the stream.
func TestFailedWriteEndsTheStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := quietHub()
		w := &fakeWriter{header: http.Header{}}
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.Serve(w, httptest.NewRequest(http.MethodGet, "/events", nil), nil)
		}()
		synctest.Wait()
		w.mu.Lock()
		w.fail = os.ErrDeadlineExceeded
		w.mu.Unlock()
		h.Publish(MonitorUpdated, "m1")
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("the handler kept going after a failed write")
		}
		if h.Stats().Clients != 0 {
			t.Fatal("the client is still subscribed")
		}
	})
}

// Subscribing, leaving, publishing and closing from many goroutines.
func TestConcurrentUse(t *testing.T) {
	h := quietHub()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 2000 {
				h.Publish(MonitorUpdated, "m1")
			}
		})
	}
	for range 8 {
		wg.Go(func() {
			for range 200 {
				c := h.subscribe()
				if c == nil {
					return // the hub closed
				}
				for range 3 {
					select {
					case <-c.frames:
					default:
					}
				}
				h.unsubscribe(c)
			}
		})
	}
	wg.Go(func() {
		time.Sleep(5 * time.Millisecond)
		h.Close()
		h.Close()
	})
	wg.Wait()
	if st := h.Stats(); st.Clients != 0 {
		t.Fatalf("clients left after Close: %+v", st)
	}
	if c := h.subscribe(); c != nil {
		t.Fatal("subscribed to a closed hub")
	}
}

// 18_PERFORMANCE.md scenario 8: fan-out to a small number of clients. One
// iteration is one event serialised, queued for eight clients and taken by
// each of them, so nothing piles up and nobody is dropped.
func BenchmarkPublish(b *testing.B) {
	h := quietHub()
	var clients []*client
	for range 8 {
		clients = append(clients, h.subscribe())
	}
	b.ReportAllocs()
	for b.Loop() {
		h.Publish(MonitorUpdated, "0123456789abcdef0123456789abcdef")
		for _, c := range clients {
			<-c.frames
		}
	}
	if st := h.Stats(); st.Clients != 8 || st.Dropped != 0 {
		b.Fatalf("stats = %+v: the benchmark did not measure a fan-out to 8 clients", st)
	}
}
