package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/store"
)

// addHeartbeat adds a heartbeat monitor and returns its id and token. Its
// interval is long, so only beats change it.
func (e *appEnv) addHeartbeat(t *testing.T) (string, string) {
	t.Helper()
	token, hash, err := store.NewHeartbeatToken()
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateMonitor(context.Background(), e.db, store.MonitorInput{
		Type: store.TypeHeartbeat, Name: "job", Enabled: true,
		Heartbeat: store.HeartbeatSettings{ExpectedIntervalSeconds: 3600, TokenHash: hash},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return id, token
}

func (e *appEnv) beats(t *testing.T, id string) int {
	t.Helper()
	var n int
	if err := e.db.Reader.QueryRow(`SELECT COUNT(*) FROM check_results WHERE monitor_id = ? AND success = 1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestHeartbeatEndpoint(t *testing.T) {
	e := newAppEnv(t)
	id, token := e.addHeartbeat(t)

	bearer := func(value string) *http.Request {
		r := httptest.NewRequest("POST", "/api/v1/heartbeat", nil)
		if value != "" {
			r.Header.Set("Authorization", value)
		}
		return r
	}
	big := strings.NewReader(strings.Repeat("x", 8<<20))
	for _, tc := range []struct {
		name string
		r    *http.Request
		code int
	}{
		{"GET path", httptest.NewRequest("GET", "/api/v1/heartbeat/"+token, nil), 204},
		{"POST path with a large body", httptest.NewRequest("POST", "/api/v1/heartbeat/"+token, big), 204},
		{"bearer", bearer("Bearer " + token), 204},
		{"bearer, any case", bearer("bearer " + token), 204},
		{"wrong token", httptest.NewRequest("GET", "/api/v1/heartbeat/"+strings.Repeat("A", 43), nil), 404},
		{"malformed token", httptest.NewRequest("POST", "/api/v1/heartbeat/nope", nil), 404},
		{"no header", bearer(""), 404},
		{"basic auth", bearer("Basic " + token), 404},
	} {
		rec := e.serve(tc.r)
		if rec.Code != tc.code {
			t.Errorf("%s: %d %q, want %d", tc.name, rec.Code, rec.Body.String(), tc.code)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control %q", tc.name, rec.Header().Get("Cache-Control"))
		}
		if tc.code == 204 && rec.Body.Len() != 0 {
			t.Errorf("%s: body %q", tc.name, rec.Body.String())
		}
	}
	if n := big.Len(); n < 8<<20-1<<20 {
		t.Errorf("the handler read %d bytes of the body", 8<<20-n)
	}

	// Four beats: four successful results, the monitor is up.
	deadline := time.Now().Add(5 * time.Second)
	for e.beats(t, id) < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	m, err := store.GetMonitor(context.Background(), e.db.Reader, id)
	if err != nil || m.State != "up" || e.beats(t, id) != 4 {
		t.Errorf("state %s, %d beats stored (%v)", m.State, e.beats(t, id), err)
	}
	if logs := e.logs.String(); strings.Contains(logs, token) || !strings.Contains(logs, "route=/api/v1/heartbeat/{token}") {
		t.Errorf("logs leak the token or miss the route:\n%s", logs)
	}
}

func TestHeartbeatEndpointRateLimit(t *testing.T) {
	e := newAppEnv(t)
	_, token := e.addHeartbeat(t)
	from := func(ip string) *http.Request {
		r := httptest.NewRequest("POST", "/api/v1/heartbeat/"+token, nil)
		r.RemoteAddr = ip + ":40000"
		return r
	}
	for i := range heartbeatLimit {
		if rec := e.serve(from("192.0.2.10")); rec.Code != 204 {
			t.Fatalf("request %d = %d", i+1, rec.Code)
		}
	}
	rec := e.serve(from("192.0.2.10"))
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "60" {
		t.Errorf("over the limit = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Failed guesses count too, and other clients are not affected.
	for range heartbeatLimit {
		e.serve(httptest.NewRequest("GET", "/api/v1/heartbeat/"+strings.Repeat("B", 43), nil))
	}
	if rec := e.serve(httptest.NewRequest("GET", "/api/v1/heartbeat/"+token, nil)); rec.Code != 429 {
		t.Errorf("guesses not limited: %d", rec.Code)
	}
	if rec := e.serve(from("192.0.2.11")); rec.Code != 204 {
		t.Errorf("other client = %d", rec.Code)
	}
}
