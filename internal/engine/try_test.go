package engine

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/secret"
	"github.com/drilonrecica/sinjal/internal/store"
)

// Try runs one check from unsaved input: nothing is stored, scheduled or
// announced (the form's "Test now").
func TestTry(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	eng := New(e.d, e.key, 1, "Sinjal/test", time.UTC, e.announce, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "s3cret" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		io.WriteString(w, "Software for people with work to do")
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	httpIn := func(body string) store.MonitorInput {
		return store.MonitorInput{Type: store.TypeHTTP, HTTP: store.HTTPConfig{URL: srv.URL, BodyContains: body}}
	}
	key := map[string]secret.String{"header.X-Api-Key": secret.String("s3cret")}

	r, err := eng.Try(ctx, httpIn("work to do"), key)
	if err != nil || !r.Success || r.Status != "200" || r.Duration <= 0 {
		t.Fatalf("up: %+v, %v", r, err)
	}
	if r, _ := eng.Try(ctx, httpIn("nothing like this"), key); r.Success || r.Kind != "body_assertion" {
		t.Errorf("wrong body: %+v", r)
	}
	if r, _ := eng.Try(ctx, httpIn(""), nil); r.Success || r.Status != "401" || r.Message == "" {
		t.Errorf("without the secret header: %+v", r)
	}

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	if r, _ := eng.Try(ctx, store.MonitorInput{Type: store.TypeTCP, TCP: store.TCPConfig{Host: "127.0.0.1", Port: port}}, nil); r.Success || r.Kind != "connect" {
		t.Errorf("tcp closed: %+v", r)
	}
	if _, err := eng.Try(ctx, store.MonitorInput{Type: store.TypeHeartbeat}, nil); err == nil {
		t.Error("a heartbeat monitor was tried")
	}
	var n int
	if err := e.d.Reader.QueryRow(`SELECT COUNT(*) FROM check_results`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d results stored (%v)", n, err)
	}
	if len(e.updated) != 0 {
		t.Errorf("announced %v", e.updated)
	}
}
