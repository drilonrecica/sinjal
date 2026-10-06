package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"version", []string{"version"}, 0, "sinjal dev\n", ""},
		{"unknown command", []string{"bogus"}, 2, "", `unknown command "bogus"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestVersionOverride(t *testing.T) {
	old := version
	version = "1.2.3"
	defer func() { version = old }()

	var stdout bytes.Buffer
	run([]string{"version"}, &stdout, &bytes.Buffer{})
	if got := stdout.String(); got != "sinjal 1.2.3\n" {
		t.Errorf("stdout = %q", got)
	}
}

func TestServeRejectsInvalidConfig(t *testing.T) {
	t.Setenv("SINJAL_LOG_LEVEL", "loud")
	t.Setenv("SINJAL_WORKERS", "0")

	var stderr bytes.Buffer
	if code := run([]string{"serve"}, &bytes.Buffer{}, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{"invalid configuration", "SINJAL_LOG_LEVEL", "SINJAL_WORKERS"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr %q is missing %q", stderr.String(), want)
		}
	}
}

// syncBuffer lets the test read logs while serve is still writing them.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// freeAddr returns a loopback address that was free a moment ago. The config
// loader rejects port 0, so tests cannot ask the kernel to choose at serve time.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func TestServeBootsAndStopsCleanly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	t.Setenv("SINJAL_DATA_DIR", dir)
	t.Setenv("SINJAL_LISTEN", freeAddr(t))

	var stderr syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- serve(ctx, &stderr) }()

	addrRe := regexp.MustCompile(`addr=(127\.0\.0\.1:\d+)`)
	var addr string
	deadline := time.Now().Add(10 * time.Second)
	for addr == "" {
		if m := addrRe.FindStringSubmatch(stderr.String()); m != nil {
			addr = m[1]
		} else if time.Now().After(deadline) {
			t.Fatalf("server did not start; log:\n%s", stderr.String())
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}

	for path, want := range map[string]string{"/healthz": "ok", "/readyz": "ready"} {
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != want {
			t.Errorf("GET %s = %d %q, want 200 %q", path, resp.StatusCode, body, want)
		}
		if resp.Header.Get("X-Request-Id") == "" {
			t.Errorf("GET %s has no X-Request-Id", path)
		}
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0; log:\n%s", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop after cancellation")
	}

	for _, want := range []string{"migration applied", "subsystem=db", "msg=request", "route=/healthz", "stopped"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("log is missing %q:\n%s", want, stderr.String())
		}
	}
	for _, p := range []string{"sinjal.db", "backups", "uploads"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s was not created: %v", p, err)
		}
	}
}

func TestServeFailsOnUnusableDataDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SINJAL_DATA_DIR", file)
	t.Setenv("SINJAL_LISTEN", freeAddr(t))

	var stderr syncBuffer
	if code := serve(context.Background(), &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not a directory") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestServeFailsWhenPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("SINJAL_DATA_DIR", t.TempDir())
	t.Setenv("SINJAL_LISTEN", ln.Addr().String())

	var stderr syncBuffer
	if code := serve(context.Background(), &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "cannot listen on") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
