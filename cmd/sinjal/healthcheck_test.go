package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// healthServer answers /healthz with code on a loopback port and returns
// the port.
func healthServer(t *testing.T, code int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	return port
}

func TestHealthcheck(t *testing.T) {
	up := healthServer(t, http.StatusOK)
	failing := healthServer(t, http.StatusServiceUnavailable)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, closed, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()

	for _, tt := range []struct {
		name, listen string
		want         int
	}{
		{"healthy, all interfaces", ":" + up, 0},
		{"healthy, a specific address", "0.0.0.0:" + up, 0},
		{"unhealthy", ":" + failing, 1},
		{"nothing listening", ":" + closed, 1},
		{"bad listen address", "nonsense", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SINJAL_LISTEN", tt.listen)
			var stdout, stderr bytes.Buffer
			if got := run([]string{"healthcheck"}, &stdout, &stderr); got != tt.want {
				t.Fatalf("exit code = %d, want %d (stderr %q)", got, tt.want, stderr.String())
			}
			if tt.want != 0 && stderr.Len() == 0 {
				t.Error("an unhealthy result says nothing on stderr")
			}
		})
	}
}

// Without SINJAL_LISTEN the check asks the default port, :8080.
func TestHealthcheckDefaultPort(t *testing.T) {
	if got, err := healthURL(""); err != nil || got != "http://127.0.0.1:8080/healthz" {
		t.Fatalf("healthURL(\"\") = %q, %v", got, err)
	}
}
