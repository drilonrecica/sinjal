// Package sse is the server-sent events hub behind GET /events
// (docs/32_SSE_EVENTS.md). Events only name what changed; the browser
// fetches the fragment again, so the server stays the single source of
// state and a missed event costs nothing but a refresh.
package sse

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Event names. The payload of each is {"monitor_id": "..."}.
const (
	MonitorCreated = "monitor.created"
	MonitorUpdated = "monitor.updated"
	MonitorDeleted = "monitor.deleted"
)

const (
	// clientBuffer is how many events may wait for one client. A client
	// that falls further behind loses its stream and reconnects.
	clientBuffer = 256
	// keepaliveEvery is the gap between comments on a quiet stream, short
	// enough for the usual 60 s idle timeout of reverse proxies. The
	// session is checked again at the same moment.
	keepaliveEvery = 20 * time.Second
	// writeTimeout bounds one write to a client that has stopped reading.
	writeTimeout = 10 * time.Second
	// opening is sent as soon as a stream starts, so the browser sees the
	// connection open at once; retry is its wait before reconnecting.
	opening = ": connected\nretry: 3000\n\n"
	ping    = ": keepalive\n\n"
)

// Hub fans events out to the connected browsers. Publishing never waits
// for a browser.
type Hub struct {
	log *slog.Logger

	mu      sync.Mutex
	clients map[*client]struct{}
	seq     uint64 // id of the last event
	closed  bool

	dropped atomic.Uint64
}

// client is one stream. Its channel is closed, by the hub only, when the
// stream has to end: the client fell behind or the hub closed.
type client struct {
	frames chan []byte
}

// Stats is a snapshot for diagnostics.
type Stats struct {
	Clients int    // open streams
	Dropped uint64 // streams ended because the client did not keep up
}

// NewHub returns a hub with no clients.
func NewHub(logger *slog.Logger) *Hub {
	return &Hub{log: logger, clients: make(map[*client]struct{})}
}

// Publish announces an event about one monitor to every stream. It never
// blocks: a client whose buffer is full is disconnected instead, so no
// browser can hold up the result processor.
func (h *Hub) Publish(event, monitorID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	// Counted even when nobody listens: ids number the events, not the
	// deliveries.
	h.seq++
	if len(h.clients) == 0 {
		return
	}
	f := frame(h.seq, event, monitorID) // built once, shared by all clients
	for c := range h.clients {
		select {
		case c.frames <- f:
		default:
			delete(h.clients, c)
			close(c.frames)
			h.dropped.Add(1)
			h.log.Warn("event stream closed: the client did not keep up", "waiting", clientBuffer)
		}
	}
}

// frame is one event on the wire. The payload is JSON on a single line, so
// no monitor id can break out of its frame.
func frame(id uint64, event, monitorID string) []byte {
	payload, _ := json.Marshal(struct {
		MonitorID string `json:"monitor_id"`
	}{monitorID})
	b := make([]byte, 0, 32+len(event)+len(payload))
	b = append(b, "id: "...)
	b = strconv.AppendUint(b, id, 10)
	b = append(b, "\nevent: "...)
	b = append(b, event...)
	b = append(b, "\ndata: "...)
	b = append(b, payload...)
	return append(b, "\n\n"...)
}

// subscribe adds a client, or returns nil once the hub is closed.
func (h *Hub) subscribe() *client {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	c := &client{frames: make(chan []byte, clientBuffer)}
	h.clients[c] = struct{}{}
	return c
}

// unsubscribe forgets a client whose handler is returning.
func (h *Hub) unsubscribe(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
}

// Close ends every stream and refuses new ones. Registered with
// http.Server.RegisterOnShutdown, it lets a graceful shutdown finish at
// once instead of waiting out its grace period on open streams.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for c := range h.clients {
		delete(h.clients, c)
		close(c.frames)
	}
}

// Stats returns the hub's counters.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Stats{Clients: len(h.clients), Dropped: h.dropped.Load()}
}

// Serve streams events to one browser until the request ends, the client
// falls behind, a write fails, the hub closes or alive reports false. alive
// is asked at every keepalive whether the session that opened the stream
// still exists; nil means always.
//
// Nothing is replayed for a Last-Event-ID: a browser that reconnects
// fetches what it shows again.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, alive func() bool) {
	c := h.subscribe()
	if c == nil {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	defer h.unsubscribe(c)

	rc := http.NewResponseController(w)
	// The server's WriteTimeout covers a whole response and would end
	// every stream after 30 s. Each write gets its own deadline instead:
	// a stream may last, but a client that has stopped reading costs one
	// bounded write. (ReadTimeout needs nothing: net/http clears the read
	// deadline once a request without a body has been read.)
	write := func(b []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Accel-Buffering", "no") // nginx: do not buffer the stream
	w.WriteHeader(http.StatusOK)
	if !write([]byte(opening)) {
		return
	}

	tick := time.NewTicker(keepaliveEvery)
	defer tick.Stop()
	var buf []byte
	for {
		select {
		case <-r.Context().Done():
			return
		case f, open := <-c.frames:
			if !open {
				return
			}
			// Everything that is waiting goes out in one write. Only this
			// goroutine receives, so what is buffered can be taken
			// without blocking.
			buf = append(buf[:0], f...)
			for waiting := len(c.frames); waiting > 0; waiting-- {
				buf = append(buf, <-c.frames...)
			}
			if !write(buf) {
				return
			}
		case <-tick.C:
			if alive != nil && !alive() {
				return
			}
			if !write([]byte(ping)) {
				return
			}
		}
	}
}
