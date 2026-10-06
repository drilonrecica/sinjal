package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
)

// resetDataDir returns a data dir holding a migrated database with one
// admin, and points SINJAL_DATA_DIR at it.
func resetDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SINJAL_DATA_DIR", dir)
	d, err := db.Open(filepath.Join(dir, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "backups"), "test", quiet); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.CreateAdmin(context.Background(), d, "admin", "correct horse battery", time.Now()); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResetAdminCommand(t *testing.T) {
	dir := resetDataDir(t)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"reset-admin", "--remove-passkeys"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr.String())
	}
	// stdout is exactly the password and a newline, so it can be captured.
	pw := strings.TrimSuffix(stdout.String(), "\n")
	if pw == "" || strings.ContainsAny(pw, " \n") {
		t.Fatalf("stdout = %q, want only the password", stdout.String())
	}

	d, err := db.Open(filepath.Join(dir, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var hash string
	if err := d.Reader.QueryRow(`SELECT password_hash FROM users WHERE login = 'admin'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if ok, _, _ := auth.VerifyPassword(pw, hash); !ok {
		t.Error("printed password does not sign in")
	}
}

func TestResetAdminCommandErrors(t *testing.T) {
	resetDataDir(t)
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{"unknown flag", []string{"reset-admin", "--bogus"}, 2, "bogus"},
		{"stray argument", []string{"reset-admin", "admin"}, 2, "takes no arguments"},
		{"unknown login", []string{"reset-admin", "--login", "nobody"}, 1, "no admin account with that login"},
		{"bad login", []string{"reset-admin", "--login", "two words"}, 1, "login"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing on failure", stdout.String())
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.wantErr)
			}
		})
	}
}

// A mistyped SINJAL_DATA_DIR must fail, not create an empty database.
func TestResetAdminCommandNeedsDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SINJAL_DATA_DIR", dir)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"reset-admin"}, &stdout, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no database at") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "sinjal.db")); err == nil {
		t.Error("reset-admin created a database")
	}
}
