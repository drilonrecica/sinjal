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
	"time"
	_ "time/tzdata" // SINJAL_TIMEZONE must work without system zoneinfo (scratch image)

	"github.com/drilonrecica/sinjal/internal/assets"
	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/config"
	"github.com/drilonrecica/sinjal/internal/datadir"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/engine"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/vault"
	"github.com/drilonrecica/sinjal/internal/web"
	"github.com/drilonrecica/sinjal/internal/web/sse"
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
	case "reset-admin":
		return resetAdmin(context.Background(), args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "sinjal: unknown command %q\n\nusage: sinjal [serve|version|reset-admin]\n", cmd)
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
	health := web.NewHealth(database.Reader, logger)

	if err := db.Migrate(ctx, database, filepath.Join(cfg.DataDir, "backups"), version, logging.Sub(logger, "db")); err != nil {
		return fatalf(stderr, "database migration: %v", err)
	}

	// Loading the key at startup enforces its permissions and catches a
	// missing key early. It encrypts stored secrets (TOTP) and derives the
	// CSRF key.
	masterKey, err := vault.LoadOrCreate(ctx, cfg.DataDir, database.Reader, logging.Sub(logger, "vault"))
	if err != nil {
		return fatalf(stderr, "%v", err)
	}

	// While no admin exists, /setup is guarded by a one-time token that only
	// the operator can read from the log (decision P0-10).
	var setupToken *auth.SetupToken
	if exists, err := auth.AdminExists(ctx, database.Reader); err != nil {
		return fatalf(stderr, "checking for an admin account: %v", err)
	} else if !exists {
		setupToken = auth.NewSetupToken()
	}

	health.SetReady() // migrations are complete; the listener opens next

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fatalf(stderr, "cannot listen on %s: %v", cfg.Listen, err)
	}
	log.Info("listening", "addr", ln.Addr().String())
	if setupToken != nil {
		log.Warn("Initial setup: " + setupBase(cfg.BaseURL, ln.Addr()) + "/setup?token=" + setupToken.Value())
	}

	sessions := auth.NewSessions(database, logging.Sub(logger, "auth"))
	// Passkeys are tied to SINJAL_BASE_URL. An address WebAuthn cannot use
	// only turns passkeys off; say so once, so it is not a silent surprise.
	passkeys := auth.NewPasskeys(database, cfg.BaseURL, logging.Sub(logger, "auth"))
	if reason := passkeys.Unavailable(); cfg.BaseURL == "" {
		log.Info("passkeys are off: " + reason)
	} else if reason != "" {
		log.Warn("passkeys are off: " + reason)
	}
	// Live updates: the engine announces changed monitors, browsers listen
	// on GET /events.
	events := sse.NewHub(logging.Sub(logger, "sse"))
	// The monitor pages schedule what they change, so the engine exists
	// before the routes; it starts once the listener is open.
	monitoring := engine.New(database, masterKey, cfg.Workers, "Sinjal/"+version, cfg.Timezone,
		func(monitorID string) { events.Publish(sse.MonitorUpdated, monitorID) }, logger)
	router := web.NewRouter(logger, cfg.TrustedProxies)
	web.Routes(router, web.App{
		Logger:   logger,
		DB:       database,
		Health:   health,
		Assets:   assets.Default,
		Sessions: sessions,
		Events:   events,
		Setup:    web.NewSetup(database, setupToken, logger),
		CSRFKey:  masterKey.Derive(web.CSRFKeyLabel),
		Vault:    masterKey,
		Passkeys: passkeys,
		Timezone: cfg.Timezone,
		Engine:   monitoring,
	})

	// Monitoring starts once the port is known to be free. Checks stop with
	// ctx: active ones are cancelled and the results already finished are
	// stored before the database closes (docs/07 "Shutdown").
	if err := monitoring.Start(ctx); err != nil {
		ln.Close()
		return fatalf(stderr, "starting the monitors: %v", err)
	}

	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		cleanupSessions(ctx, sessions, logging.Sub(logger, "auth"))
	}()

	srv := web.NewServer(cfg.Listen, router)
	// Open event streams never finish by themselves; without this a
	// graceful shutdown would wait out its whole grace period on them.
	srv.RegisterOnShutdown(events.Close)
	err = web.Run(ctx, srv, ln, web.ShutdownGrace, logger)
	// ctx is done once Run returns; let the jobs finish before the DB closes.
	<-cleanupDone
	monitoring.Wait()
	if err != nil {
		return fatalf(stderr, "http server: %v", err)
	}
	log.Info("stopped")
	return 0
}

// setupBase is SINJAL_BASE_URL, or http://localhost:<port> when it is unset.
func setupBase(baseURL string, addr net.Addr) string {
	if baseURL != "" {
		return baseURL
	}
	_, port, _ := net.SplitHostPort(addr.String())
	return "http://localhost:" + port
}

// sessionCleanupInterval: expired sessions are already rejected on lookup;
// deleting them only keeps the table small.
const sessionCleanupInterval = 24 * time.Hour

// cleanupSessions deletes expired sessions at startup and then daily until
// ctx is cancelled. It moves into the daily job runner in M6-04.
func cleanupSessions(ctx context.Context, sessions *auth.Sessions, log *slog.Logger) {
	t := time.NewTicker(sessionCleanupInterval)
	defer t.Stop()
	for {
		if n, err := sessions.DeleteExpired(ctx, time.Now()); err != nil {
			if ctx.Err() == nil {
				log.Error("expired session cleanup failed", "error", err)
			}
		} else if n > 0 {
			log.Info("deleted expired sessions", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
