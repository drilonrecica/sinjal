// Package datadir prepares the persistent data directory (docs/09_DATABASE.md).
package datadir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Subdirectories created under the data directory.
var subdirs = []string{"backups", "uploads"}

// Init creates the data directory and its backups/ and uploads/
// subdirectories (mode 0700) and verifies the directory is writable.
// Directories that already exist are left as they are.
func Init(dir string) error {
	if err := ensureDir(dir); err != nil {
		return err
	}
	for _, name := range subdirs {
		if err := ensureDir(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return probeWritable(dir)
}

func ensureDir(path string) error {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("data path %q exists but is not a directory", path)
		}
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("cannot access %q: %w", path, err)
	}

	// MkdirAll would apply the umask to parents too; the data root's parents
	// are the operator's business, so create them normally and only
	// tighten the directory we own.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create %q: %w%s", path, err, hint(err))
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return fmt.Errorf("cannot create %q: %w%s", path, err, hint(err))
	}
	// Mkdir is subject to the umask, which can only remove bits, but be explicit.
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("cannot set permissions on %q: %w", path, err)
	}
	return nil
}

// probeWritable proves we can create, write, sync and remove a file in dir.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".write-probe-*")
	if err != nil {
		return fmt.Errorf("data directory %q is not writable: %w%s", dir, err, hint(err))
	}
	name := f.Name()
	_, werr := f.Write([]byte("sinjal"))
	if serr := f.Sync(); werr == nil {
		werr = serr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	rerr := os.Remove(name)
	if werr != nil {
		return fmt.Errorf("data directory %q is not writable: %w", dir, werr)
	}
	if rerr != nil {
		return fmt.Errorf("data directory %q: cannot remove probe file: %w", dir, rerr)
	}
	return nil
}

func hint(err error) string {
	if errors.Is(err, fs.ErrPermission) {
		return " (check that the user running sinjal owns it; in Docker, check the volume's ownership)"
	}
	return ""
}
