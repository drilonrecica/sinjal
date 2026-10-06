// Package integration holds black-box tests that run the real sinjal binary.
//
// Milestone 0 scenario: boot from an empty data directory, create the
// database, record every embedded migration, answer health checks, render the app shell,
// serve hashed assets, stop cleanly on SIGTERM and restart without a backup.
package integration

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

const (
	startTimeout = 15 * time.Second
	stopTimeout  = 15 * time.Second
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// binary builds cmd/sinjal once per test run and returns its path.
func binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sinjal-integration-")
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

func TestMain(m *testing.M) {
	code := m.Run()
	if binPath != "" {
		os.RemoveAll(filepath.Dir(binPath))
	}
	os.Exit(code)
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
	t    *testing.T
	cmd  *exec.Cmd
	base string
	logs *lockedBuffer
	done chan error
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// start launches `sinjal serve` with a scrubbed environment and waits until
// /readyz answers 200.
func start(t *testing.T, dataDir string) *server {
	t.Helper()
	addr := freeAddr(t)
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
	s := &server{t: t, cmd: cmd, base: "http://" + addr, logs: logs, done: make(chan error, 1)}
	go func() { s.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			<-s.done
		}
	})

	deadline := time.Now().Add(startTimeout)
	for {
		select {
		case err := <-s.done:
			t.Fatalf("server exited during startup: %v\n%s", err, logs)
		default:
		}
		if resp, err := http.Get(s.base + "/readyz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("server not ready within %v\n%s", startTimeout, logs)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// stop sends SIGTERM and returns the process's exit error (nil = exit 0).
func (s *server) stop() error {
	s.t.Helper()
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		s.t.Fatal(err)
	}
	select {
	case err := <-s.done:
		s.done <- err // keep the channel readable for Cleanup
		return err
	case <-time.After(stopTimeout):
		s.t.Fatalf("server did not stop within %v after SIGTERM\n%s", stopTimeout, s.logs)
		return nil
	}
}

// records parses the JSON log lines.
func (s *server) records() []map[string]any {
	s.t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			s.t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func hasMsg(recs []map[string]any, msg string) bool {
	for _, r := range recs {
		if r["msg"] == msg {
			return true
		}
	}
	return false
}

func body(t *testing.T, client *http.Client, url string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, b
}

func schemaVersions(t *testing.T, dbPath string) []int {
	t.Helper()
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	rows, err := d.Reader.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}

	var n int
	if err := d.Reader.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='system_settings'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("system_settings table missing (n=%d, err=%v)", n, err)
	}
	return out
}

// embeddedMigrationVersions derives the expected schema versions from the
// migration files, so the test does not need editing for every milestone.
func embeddedMigrationVersions(t *testing.T) []int {
	t.Helper()
	files, err := filepath.Glob("../../internal/db/migrations/[0-9][0-9][0-9]_*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("cannot list migrations: %v (%d files)", err, len(files))
	}
	sort.Strings(files)
	var out []int
	for _, f := range files {
		v, err := strconv.Atoi(filepath.Base(f)[:3])
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

func TestMilestone0(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test builds and runs the binary")
	}
	dataDir := filepath.Join(t.TempDir(), "data") // empty: does not exist yet
	dbPath := filepath.Join(dataDir, "sinjal.db")
	wantVersions := embeddedMigrationVersions(t)

	// ---- first boot from an empty data directory --------------------------
	s := start(t, dataDir)

	t.Run("health endpoints", func(t *testing.T) {
		for path, want := range map[string]string{"/healthz": "ok", "/readyz": "ready"} {
			resp, b := body(t, http.DefaultClient, s.base+path, nil)
			if resp.StatusCode != 200 || strings.TrimSpace(string(b)) != want {
				t.Errorf("GET %s = %d %q, want 200 %q", path, resp.StatusCode, b, want)
			}
		}
	})

	t.Run("root renders the app shell", func(t *testing.T) {
		resp, b := body(t, http.DefaultClient, s.base+"/", nil)
		page := string(b)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("GET / = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		for _, want := range []string{
			"<title>Overview — Sinjal</title>", `<main id="main"`, `<nav class="nav" aria-label="Primary">`,
			`<a class="skip-link" href="#main">`, `data-theme="carbon"`, `data-density="comfortable"`,
			`<h1>Overview</h1>`,
		} {
			if !strings.Contains(page, want) {
				t.Errorf("page is missing %q", want)
			}
		}
		links := regexp.MustCompile(`<a class="nav-link" href="([^"]+)"( aria-current="page")?>([^<]+)</a>`).FindAllStringSubmatch(page, -1)
		var labels []string
		for _, l := range links {
			labels = append(labels, l[3])
			if (l[3] == "Overview") != (l[2] != "") {
				t.Errorf("aria-current on %q = %v", l[3], l[2] != "")
			}
		}
		want := "Overview,Monitors,Incidents,Status Pages,Notifications,Maintenance,Settings"
		if strings.Join(labels, ",") != want {
			t.Errorf("nav = %v, want %s", labels, want)
		}
	})

	t.Run("assets are hashed, immutable and gzip", func(t *testing.T) {
		_, b := body(t, http.DefaultClient, s.base+"/", nil)
		urls := regexp.MustCompile(`(?:href|src)="(/static/[^"]+)"`).FindAllStringSubmatch(string(b), -1)
		if len(urls) != 4 {
			t.Fatalf("page links %d static assets, want 4 (tokens, base, shell css + htmx)", len(urls))
		}
		raw := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		for _, m := range urls {
			url := s.base + m[1]
			plainResp, plain := body(t, raw, url, nil)
			if plainResp.StatusCode != 200 {
				t.Errorf("%s = %d", m[1], plainResp.StatusCode)
				continue
			}
			if cc := plainResp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
				t.Errorf("%s Cache-Control = %q", m[1], cc)
			}
			if !regexp.MustCompile(`\.[0-9a-f]{16}\.(css|js)$`).MatchString(m[1]) {
				t.Errorf("%s is not content-hashed", m[1])
			}
			gzResp, gz := body(t, raw, url, map[string]string{"Accept-Encoding": "gzip"})
			if gzResp.Header.Get("Content-Encoding") != "gzip" {
				t.Errorf("%s was not gzip-compressed", m[1])
				continue
			}
			zr, err := gzip.NewReader(bytes.NewReader(gz))
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := io.ReadAll(zr); !bytes.Equal(got, plain) {
				t.Errorf("%s: gzip body differs from identity body", m[1])
			}
		}
	})

	t.Run("unknown paths are 404 and unhashed asset names are not served", func(t *testing.T) {
		for _, p := range []string{"/nope", "/static/css/base.css"} {
			if resp, _ := body(t, http.DefaultClient, s.base+p, nil); resp.StatusCode != 404 {
				t.Errorf("GET %s = %d, want 404", p, resp.StatusCode)
			}
		}
	})

	// ---- graceful stop ----------------------------------------------------
	if err := s.stop(); err != nil {
		t.Fatalf("SIGTERM exit: %v\n%s", err, s.logs)
	}
	first := s.records()

	t.Run("first boot log", func(t *testing.T) {
		if !hasMsg(first, "migration applied") || !hasMsg(first, "listening") || !hasMsg(first, "stopped") {
			t.Errorf("expected migration applied / listening / stopped records:\n%s", s.logs)
		}
		for _, r := range first {
			if r["level"] == "ERROR" || r["msg"] == "panic recovered" {
				t.Errorf("unexpected error record: %v", r)
			}
			if r["subsystem"] == nil {
				t.Errorf("record without subsystem: %v", r)
			}
		}
		var applied []float64
		for _, r := range first {
			if r["msg"] == "migration applied" {
				applied = append(applied, r["version"].(float64))
			}
		}
		if len(applied) != len(wantVersions) {
			t.Fatalf("applied migrations %v, want one per embedded file %v", applied, wantVersions)
		}
		for i, v := range applied {
			if int(v) != wantVersions[i] {
				t.Errorf("migration %d applied as version %v, want %d", i, v, wantVersions[i])
			}
		}
		if hasMsg(first, "pre-migration backup written") {
			t.Error("a brand-new database must not be backed up")
		}
	})

	t.Run("data directory state", func(t *testing.T) {
		for path, want := range map[string]os.FileMode{
			dataDir: 0o700, filepath.Join(dataDir, "backups"): 0o700, filepath.Join(dataDir, "uploads"): 0o700,
			dbPath: 0o600,
		} {
			info, err := os.Stat(path)
			if err != nil {
				t.Errorf("%s: %v", path, err)
				continue
			}
			if info.Mode().Perm() != want {
				t.Errorf("%s mode = %o, want %o", filepath.Base(path), info.Mode().Perm(), want)
			}
		}
		if got := entries(t, filepath.Join(dataDir, "backups")); len(got) != 0 {
			t.Errorf("backups = %v, want none", got)
		}
		if got := schemaVersions(t, dbPath); !sameInts(got, wantVersions) {
			t.Errorf("schema_migrations = %v, want %v", got, wantVersions)
		}
	})

	// ---- restart on the same data directory -------------------------------
	s2 := start(t, dataDir)
	if resp, b := body(t, http.DefaultClient, s2.base+"/healthz", nil); resp.StatusCode != 200 || strings.TrimSpace(string(b)) != "ok" {
		t.Errorf("after restart GET /healthz = %d %q", resp.StatusCode, b)
	}
	if err := s2.stop(); err != nil {
		t.Fatalf("second SIGTERM exit: %v\n%s", err, s2.logs)
	}
	second := s2.records()

	t.Run("restart is a no-op for the database", func(t *testing.T) {
		if hasMsg(second, "migration applied") || hasMsg(second, "pre-migration backup written") {
			t.Errorf("restart must not migrate or back up:\n%s", s2.logs)
		}
		if !hasMsg(second, "stopped") {
			t.Errorf("second run did not stop cleanly:\n%s", s2.logs)
		}
		if got := entries(t, filepath.Join(dataDir, "backups")); len(got) != 0 {
			t.Errorf("backups after restart = %v, want none", got)
		}
		if got := schemaVersions(t, dbPath); !sameInts(got, wantVersions) {
			t.Errorf("schema_migrations after restart = %v, want %v", got, wantVersions)
		}
	})
}
