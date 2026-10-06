// Package vault owns the master key and the encrypted envelope used for
// secrets at rest (docs/13_AUTH_SECURITY.md).
package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// KeyFile is the master key's file name inside the data directory.
const KeyFile = "master.key"

const keySize = 32 // AES-256

const redacted = "[REDACTED]"

// Key is the loaded master key. The raw bytes are not kept; only the AEAD
// built from them, and every formatting path prints "[REDACTED]".
type Key struct {
	aead cipher.AEAD
}

func (*Key) String() string   { return redacted }
func (*Key) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (*Key) LogValue() slog.Value { return slog.StringValue(redacted) }

func newKey(raw []byte) (*Key, error) {
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return &Key{aead: aead}, nil
}

// LoadOrCreate loads <dataDir>/master.key, creating it only when it does
// not exist and the database holds no encrypted values. It never replaces
// an existing or unreadable key file (docs/19_RELIABILITY.md).
func LoadOrCreate(ctx context.Context, dataDir string, db *sql.DB, log *slog.Logger) (*Key, error) {
	path := filepath.Join(dataDir, KeyFile)

	info, err := os.Stat(path)
	switch {
	case err == nil:
		return load(path, info)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("cannot access master key %q: %w", path, err)
	}

	enc, err := hasEncryptedData(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("master key %q is missing and the database could not be checked for encrypted data: %w", path, err)
	}
	if enc {
		return nil, fmt.Errorf("master key %q is missing but the database holds encrypted secrets; "+
			"restore master.key from your backup into the data directory. "+
			"Sinjal never generates a replacement key, because existing secrets would become unreadable", path)
	}

	raw := make([]byte, keySize)
	rand.Read(raw) // never returns an error (crypto/rand, Go 1.24+)
	if err := create(path, raw); err != nil {
		return nil, err
	}
	log.Warn("generated a new master key; back it up together with the database, secrets cannot be recovered without it", "path", path)
	return newKey(raw)
}

func load(path string, info fs.FileInfo) (*Key, error) {
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("master key %q is not a regular file", path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("master key %q has permissions %04o; it must be readable by its owner only "+
			"(run: chmod 600 %s, and check that the user running sinjal owns it)", path, perm, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read master key %q: %w", path, err)
	}
	if len(raw) != keySize {
		return nil, fmt.Errorf("master key %q is %d bytes, want %d; the file is corrupt or truncated, restore it from your backup", path, len(raw), keySize)
	}
	return newKey(raw)
}

// create writes the key to a temporary file and links it into place. The link
// fails if path already exists, so an existing key is never overwritten, and
// readers never see a partially written file.
func create(path string, raw []byte) (err error) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".master.key-*")
	if err != nil {
		return fmt.Errorf("cannot create master key in %q: %w", dir, err)
	}
	tmp := f.Name()
	defer func() {
		if rerr := os.Remove(tmp); err == nil && rerr != nil {
			err = fmt.Errorf("cannot remove temporary key file %q: %w", tmp, rerr)
		}
	}()

	// CreateTemp already uses 0600; be explicit since the mode is the point.
	werr := f.Chmod(0o600)
	if werr == nil {
		_, werr = f.Write(raw)
	}
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("cannot write master key %q: %w", tmp, werr)
	}
	if err := os.Link(tmp, path); err != nil {
		return fmt.Errorf("cannot create master key %q: %w", path, err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("cannot open %q to sync: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("cannot sync %q: %w", dir, err)
	}
	return nil
}

// hasEncryptedData reports whether any column named *_enc holds a value.
// Encrypted columns follow that naming convention (spec/schema.sql), so new
// tables are covered without changes here.
func hasEncryptedData(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT m.name, c.name
		FROM sqlite_schema AS m, pragma_table_info(m.name) AS c
		WHERE m.type = 'table' AND c.name LIKE '%\_enc' ESCAPE '\'`)
	if err != nil {
		return false, err
	}
	type col struct{ table, column string }
	var cols []col
	for rows.Next() {
		var c col
		if err := rows.Scan(&c.table, &c.column); err != nil {
			rows.Close()
			return false, err
		}
		cols = append(cols, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	for _, c := range cols {
		q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE %s IS NOT NULL)`, quoteIdent(c.table), quoteIdent(c.column))
		var found bool
		if err := db.QueryRowContext(ctx, q).Scan(&found); err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
