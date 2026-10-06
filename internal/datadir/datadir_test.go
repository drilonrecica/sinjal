package datadir

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", path)
	}
	return info.Mode().Perm()
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
}

func TestInitCreatesLayout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, filepath.Join(dir, "backups"), filepath.Join(dir, "uploads")} {
		if got := mode(t, p); got != 0o700 {
			t.Errorf("%s mode = %o, want 700", p, got)
		}
	}
}

func TestInitIgnoresLooseUmask(t *testing.T) {
	old := syscall.Umask(0) // process-wide; these tests do not run in parallel
	defer syscall.Umask(old)

	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, dir); got != 0o700 {
		t.Errorf("mode = %o, want 700", got)
	}
}

func TestInitIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	for range 2 {
		if err := Init(dir); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInitKeepsExistingModes(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, dir); got != 0o755 {
		t.Errorf("existing directory mode changed to %o", got)
	}
	if got := mode(t, filepath.Join(dir, "backups")); got != 0o700 {
		t.Errorf("backups mode = %o, want 700", got)
	}
}

func TestInitLeavesNoProbeFile(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".write-probe") {
			t.Errorf("probe file left behind: %s", e.Name())
		}
	}
}

func TestInitPathIsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := Init(path)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v, want a not-a-directory error", err)
	}
}

func TestInitSubdirIsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "backups"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := Init(dir)
	if err == nil || !strings.Contains(err.Error(), "backups") {
		t.Fatalf("err = %v, want an error naming backups", err)
	}
}

func TestInitReadOnlyParent(t *testing.T) {
	skipIfRoot(t)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0o700)

	dir := filepath.Join(parent, "data")
	err := Init(dir)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "owns it") {
		t.Errorf("error %q should name the path and give a hint", err)
	}
}

func TestInitReadOnlyExistingDir(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	for _, sub := range subdirs {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	err := Init(dir)
	if err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("err = %v, want a not-writable error", err)
	}
}
