package integration

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

// keyGeneratedMsg is the WARN record logged when a new master key is created.
const keyGeneratedMsg = "generated a new master key; back it up together with the database, secrets cannot be recovered without it"

// TestMissingMasterKeyWithEncryptedData checks that startup refuses to run,
// and does not create a replacement key, when master.key is gone but the
// database already holds encrypted values.
func TestMissingMasterKeyWithEncryptedData(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test builds and runs the binary")
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	keyPath := filepath.Join(dataDir, "master.key")

	s := start(t, dataDir)
	if err := s.stop(); err != nil {
		t.Fatalf("SIGTERM exit: %v\n%s", err, s.logs)
	}

	d, err := db.Open(filepath.Join(dataDir, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Writer.Exec(`INSERT INTO users (id, login, role, totp_secret_enc, created_at, updated_at)
		VALUES ('u1', 'admin', 'admin', x'01', 'now', 'now')`)
	d.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary(t), "serve")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"SINJAL_DATA_DIR=" + dataDir,
		"SINJAL_LISTEN=" + freeAddr(t),
		"SINJAL_LOG_FORMAT=json",
	}
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("server did not exit; it should refuse to start:\n%s", out)
	}
	if err == nil {
		t.Fatalf("server exited 0, want a startup failure:\n%s", out)
	}
	if !strings.Contains(string(out), "restore master.key from your backup") {
		t.Errorf("exit output lacks restore guidance:\n%s", out)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Errorf("a replacement master key was created (stat err %v)", err)
	}
}

// TestProxyTrust is integration scenario 19: X-Forwarded-* headers are
// honoured only from peers listed in SINJAL_TRUSTED_PROXIES.
func TestProxyTrust(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test builds and runs the binary")
	}
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"no trusted proxies", nil, "127.0.0.1"},
		{"untrusted peer", []string{"SINJAL_TRUSTED_PROXIES=10.0.0.0/8"}, "127.0.0.1"},
		{"trusted peer", []string{"SINJAL_TRUSTED_PROXIES=127.0.0.1"}, "203.0.113.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := start(t, filepath.Join(t.TempDir(), "data"), tc.env...)
			resp, _ := body(t, http.DefaultClient, s.base+"/healthz", map[string]string{
				"X-Forwarded-For": "198.51.100.1, 203.0.113.9",
			})
			if resp.StatusCode != 200 {
				t.Fatalf("GET /healthz = %d", resp.StatusCode)
			}
			if err := s.stop(); err != nil {
				t.Fatalf("SIGTERM exit: %v", err)
			}
			var got []any
			for _, r := range s.records() {
				if r["msg"] == "request" && r["route"] == "/healthz" {
					got = append(got, r["client_ip"])
				}
			}
			if len(got) == 0 || got[len(got)-1] != tc.want {
				t.Errorf("client_ip of /healthz requests = %v, want last = %s", got, tc.want)
			}
		})
	}
}

// setupURL returns the URL from the "Initial setup: ..." WARN line, or "".
func setupURL(s *server) string {
	for _, r := range s.records() {
		if msg, _ := r["msg"].(string); strings.HasPrefix(msg, "Initial setup: ") && r["level"] == "WARN" {
			return strings.TrimPrefix(msg, "Initial setup: ")
		}
	}
	return ""
}

// TestInitialSetup covers the setup token flow end to end: the token is
// logged once, rotates on restart, guards /setup, and setup closes for good
// after the admin is created.
func TestInitialSetup(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test builds and runs the binary")
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	s := start(t, dataDir)
	first := setupURL(s)
	if err := s.stop(); err != nil {
		t.Fatal(err)
	}
	s = start(t, dataDir)
	link := setupURL(s)
	if first == "" || link == "" || first == link {
		t.Fatalf("setup links %q / %q: want one per start, rotated on restart\n%s", first, link, s.logs)
	}
	u, err := url.Parse(link)
	if err != nil || u.Host == "" || !strings.HasPrefix(u.Host, "localhost:") || u.Path != "/setup" {
		t.Fatalf("setup link %q", link)
	}
	token := u.Query().Get("token")
	get := s.base + "/setup?token="

	if resp, _ := body(t, noRedirect, get+url.QueryEscape(first[strings.Index(first, "token=")+6:]), nil); resp.StatusCode != 403 {
		t.Errorf("old token after restart = %d, want 403", resp.StatusCode)
	}
	if resp, b := body(t, noRedirect, get+token, nil); resp.StatusCode != 200 || !strings.Contains(string(b), "Create the admin account") {
		t.Fatalf("GET /setup with token = %d", resp.StatusCode)
	}

	resp, err := noRedirect.PostForm(s.base+"/setup", url.Values{
		"token": {token}, "login": {"admin"},
		"password": {"correct horse battery"}, "confirm": {"correct horse battery"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/login" {
		t.Fatalf("POST /setup = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := body(t, noRedirect, get+token, nil); resp.StatusCode != 404 {
		t.Errorf("GET /setup after setup = %d, want 404", resp.StatusCode)
	}
	if err := s.stop(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.logs.String(), "correct horse battery") {
		t.Error("password reached the logs")
	}

	s = start(t, dataDir)
	if link := setupURL(s); link != "" {
		t.Errorf("setup link logged although an admin exists: %q", link)
	}
	if resp, _ := body(t, noRedirect, s.base+"/setup?token="+token, nil); resp.StatusCode != 404 {
		t.Errorf("GET /setup after restart = %d, want 404", resp.StatusCode)
	}
}
