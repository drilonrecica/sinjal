package vault

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/drilonrecica/sinjal/internal/db"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// migrated returns a data dir and a fully migrated database inside it.
func migrated(t *testing.T) (string, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(dir, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "backups"), "test", quiet); err != nil {
		t.Fatal(err)
	}
	return dir, d
}

func loadKey(t *testing.T, dir string, d *db.DB) (*Key, error) {
	t.Helper()
	return LoadOrCreate(context.Background(), dir, d.Reader, quiet)
}

func readKeyFile(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q, got nil", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error %q does not contain %q", err, substr)
	}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
}

func TestCreatesKey(t *testing.T) {
	dir, d := migrated(t)
	var logs bytes.Buffer
	k, err := LoadOrCreate(context.Background(), dir, d.Reader, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if k == nil {
		t.Fatal("nil key")
	}
	info, err := os.Stat(filepath.Join(dir, KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
	if n := len(readKeyFile(t, dir)); n != keySize {
		t.Fatalf("key is %d bytes, want %d", n, keySize)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "back it up") {
		t.Fatalf("missing backup warning, logs: %s", logs.String())
	}
	// No temporary files are left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".master.key-") {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
}

func TestCreatesKeyWithLooseUmask(t *testing.T) {
	dir, d := migrated(t)
	old := syscall.Umask(0) // process-wide; these tests do not run in parallel
	defer syscall.Umask(old)
	if _, err := loadKey(t, dir, d); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
}

func TestReloadKeepsKey(t *testing.T) {
	dir, d := migrated(t)
	if _, err := loadKey(t, dir, d); err != nil {
		t.Fatal(err)
	}
	first := readKeyFile(t, dir)
	info1, _ := os.Stat(filepath.Join(dir, KeyFile))

	var logs bytes.Buffer
	if _, err := LoadOrCreate(context.Background(), dir, d.Reader, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, readKeyFile(t, dir)) {
		t.Fatal("key file changed on reload")
	}
	info2, _ := os.Stat(filepath.Join(dir, KeyFile))
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatal("key file was rewritten on reload")
	}
	if logs.Len() != 0 {
		t.Fatalf("reload logged: %s", logs.String())
	}
}

func TestLoadsExistingKey(t *testing.T) {
	dir, d := migrated(t)
	raw := bytes.Repeat([]byte{7}, keySize)
	if err := os.WriteFile(filepath.Join(dir, KeyFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKey(t, dir, d); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, readKeyFile(t, dir)) {
		t.Fatal("existing key was modified")
	}
}

func TestFollowsSymlink(t *testing.T) {
	dir, d := migrated(t)
	target := filepath.Join(t.TempDir(), "mounted.key")
	if err := os.WriteFile(target, bytes.Repeat([]byte{1}, keySize), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, KeyFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKey(t, dir, d); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsLoosePermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			dir, d := migrated(t)
			path := filepath.Join(dir, KeyFile)
			raw := bytes.Repeat([]byte{7}, keySize)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			_, err := loadKey(t, dir, d)
			wantErr(t, err, "chmod 600")
			if !bytes.Equal(raw, readKeyFile(t, dir)) {
				t.Fatal("key file was modified")
			}
		})
	}
}

func TestRejectsWrongSize(t *testing.T) {
	for _, n := range []int{0, 16, keySize - 1, keySize + 1, 64} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			dir, d := migrated(t)
			path := filepath.Join(dir, KeyFile)
			raw := bytes.Repeat([]byte{7}, n)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadKey(t, dir, d)
			wantErr(t, err, "corrupt or truncated")
			if !bytes.Equal(raw, readKeyFile(t, dir)) {
				t.Fatal("key file was modified")
			}
		})
	}
}

func TestRejectsDirectory(t *testing.T) {
	dir, d := migrated(t)
	if err := os.Mkdir(filepath.Join(dir, KeyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := loadKey(t, dir, d)
	wantErr(t, err, "not a regular file")
}

func TestUnreadableKeyIsNotReplaced(t *testing.T) {
	skipIfRoot(t)
	dir, d := migrated(t)
	path := filepath.Join(dir, KeyFile)
	raw := bytes.Repeat([]byte{7}, keySize)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	_, err := loadKey(t, dir, d)
	wantErr(t, err, "cannot read master key")
	os.Chmod(path, 0o600)
	if !bytes.Equal(raw, readKeyFile(t, dir)) {
		t.Fatal("key file was replaced")
	}
}

func TestInaccessibleDirIsNotTreatedAsMissing(t *testing.T) {
	skipIfRoot(t)
	dir, d := migrated(t)
	keyDir := filepath.Join(dir, "locked")
	if err := os.Mkdir(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, KeyFile), bytes.Repeat([]byte{7}, keySize), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(keyDir, 0o700) })
	_, err := LoadOrCreate(context.Background(), keyDir, d.Reader, quiet)
	wantErr(t, err, "cannot access master key")
}

func TestMissingKeyWithEncryptedDataIsFatal(t *testing.T) {
	dir, d := migrated(t)
	if _, err := d.Writer.Exec(`INSERT INTO users (id, login, role, totp_secret_enc, created_at, updated_at)
		VALUES ('u1', 'admin', 'admin', x'01', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	_, err := loadKey(t, dir, d)
	wantErr(t, err, "restore master.key from your backup")
	if _, statErr := os.Stat(filepath.Join(dir, KeyFile)); !os.IsNotExist(statErr) {
		t.Fatalf("a key file was created: %v", statErr)
	}
}

func TestNullEncryptedColumnsAllowCreation(t *testing.T) {
	dir, d := migrated(t)
	if _, err := d.Writer.Exec(`INSERT INTO users (id, login, role, created_at, updated_at)
		VALUES ('u1', 'admin', 'admin', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKey(t, dir, d); err != nil {
		t.Fatal(err)
	}
}

func TestHasEncryptedDataFindsAnyEncColumn(t *testing.T) {
	_, d := migrated(t)
	ctx := context.Background()
	// A table added by a later migration is covered by the naming convention.
	if _, err := d.Writer.Exec(`CREATE TABLE "odd ""name""" (id INTEGER, config_enc BLOB, plain_encoding TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer.Exec(`INSERT INTO "odd ""name""" VALUES (1, NULL, 'x')`); err != nil {
		t.Fatal(err)
	}
	if got, err := hasEncryptedData(ctx, d.Reader); err != nil || got {
		t.Fatalf("hasEncryptedData = %v, %v; want false (NULL value, non-matching column)", got, err)
	}
	if _, err := d.Writer.Exec(`UPDATE "odd ""name""" SET config_enc = x'00'`); err != nil {
		t.Fatal(err)
	}
	if got, err := hasEncryptedData(ctx, d.Reader); err != nil || !got {
		t.Fatalf("hasEncryptedData = %v, %v; want true", got, err)
	}
}

func TestKeyIsRedacted(t *testing.T) {
	dir, d := migrated(t)
	k, err := loadKey(t, dir, d)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("x", "key", k)
	outputs := []string{fmt.Sprint(k), fmt.Sprintf("%v %+v %#v %s", k, k, k, k), logs.String()}
	for _, out := range outputs {
		if !strings.Contains(out, redacted) {
			t.Fatalf("output not redacted: %s", out)
		}
	}
	raw := readKeyFile(t, dir)
	for _, out := range outputs {
		if strings.Contains(out, fmt.Sprintf("%x", raw)) || bytes.Contains([]byte(out), raw) {
			t.Fatalf("key bytes leaked: %s", out)
		}
	}
}
