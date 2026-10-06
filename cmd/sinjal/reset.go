package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/config"
	"github.com/drilonrecica/sinjal/internal/db"
)

// resetAdmin implements `sinjal reset-admin` (decision P0-11): account
// recovery for an operator with host access. It runs ordinary transactional
// writes against the database in SINJAL_DATA_DIR, so it is safe while the
// server is running. It needs neither migrations nor the master key.
func resetAdmin(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reset-admin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	login := fs.String("login", "", "admin login (required only when more than one admin exists)")
	removePasskeys := fs.Bool("remove-passkeys", false, "also delete the admin's passkeys")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "sinjal: reset-admin takes no arguments, got %q\n", fs.Arg(0))
		return 2
	}

	cfg, err := config.Load(os.Getenv, os.Environ)
	if err != nil {
		return fatalf(stderr, "invalid configuration:\n%v", err)
	}
	// db.Open would create an empty database; recovery must not.
	path := filepath.Join(cfg.DataDir, "sinjal.db")
	if _, err := os.Stat(path); err != nil {
		return fatalf(stderr, "no database at %s (is SINJAL_DATA_DIR correct?)", path)
	}
	database, err := db.Open(path)
	if err != nil {
		return fatalf(stderr, "%v", err)
	}
	defer database.Close()

	password, err := auth.ResetAdmin(ctx, database, *login, *removePasskeys, time.Now())
	var inputErr *auth.InputError
	switch {
	case errors.As(err, &inputErr), errors.Is(err, auth.ErrNoAdmin), errors.Is(err, auth.ErrAdminNotFound), errors.Is(err, auth.ErrAdminAmbiguous):
		return fatalf(stderr, "reset-admin: %v", err)
	case err != nil:
		return fatalf(stderr, "reset-admin failed: %v", err)
	}
	// Only the password goes to stdout, so `pw=$(sinjal reset-admin)` works.
	fmt.Fprintln(stdout, password)
	fmt.Fprintln(stderr, "sinjal: admin password reset; sign in with the password above and change it. TOTP is off and all sessions are signed out.")
	return 0
}
