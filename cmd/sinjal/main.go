package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	_ "time/tzdata" // SINJAL_TIMEZONE must work without system zoneinfo (scratch image)

	"github.com/drilonrecica/sinjal/internal/config"
	"github.com/drilonrecica/sinjal/internal/datadir"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/web"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches subcommands and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "serve":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return serve(ctx, stderr)
	case "version":
		fmt.Fprintf(stdout, "sinjal %s\n", version)
		return 0
	default:
		fmt.Fprintf(stderr, "sinjal: unknown command %q\n\nusage: sinjal [serve|version]\n", cmd)
		return 2
	}
}

// fatalf reports an unrecoverable startup error and returns exit code 1.
func fatalf(stderr io.Writer, format string, a ...any) int {
	fmt.Fprintf(stderr, "sinjal: "+format+"\n", a...)
	return 1
}

// serve runs the application until ctx is cancelled (SIGINT/SIGTERM in
// production) and returns the process exit code.
func serve(ctx context.Context, stderr io.Writer) int {
	cfg, err := config.Load(os.Getenv, os.Environ)
	if err != nil {
		return fatalf(stderr, "invalid configuration:\n%v", err)
	}

	logger := logging.New(stderr, cfg.LogFormat, cfg.LogLevel)
	slog.SetDefault(logger)
	log := logging.Sub(logger, "main")
	log.Info("starting", "version", version, "data_dir", cfg.DataDir)

	if err := datadir.Init(cfg.DataDir); err != nil {
		return fatalf(stderr, "%v", err)
	}

	database, err := db.Open(filepath.Join(cfg.DataDir, "sinjal.db"))
	if err != nil {
		return fatalf(stderr, "%v", err)
	}
	defer database.Close()

	if err := db.Migrate(ctx, database, filepath.Join(cfg.DataDir, "backups"), version, logging.Sub(logger, "db")); err != nil {
		return fatalf(stderr, "database migration: %v", err)
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fatalf(stderr, "cannot listen on %s: %v", cfg.Listen, err)
	}
	log.Info("listening", "addr", ln.Addr().String())

	srv := web.NewServer(cfg.Listen, web.NewRouter(logger))
	if err := web.Run(ctx, srv, ln, web.ShutdownGrace, logger); err != nil {
		return fatalf(stderr, "http server: %v", err)
	}
	log.Info("stopped")
	return 0
}
