package integration

import (
	"context"
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
