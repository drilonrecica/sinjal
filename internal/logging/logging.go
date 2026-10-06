// Package logging builds the process logger on log/slog.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New returns a logger writing to w. format is "text" (default) or "json";
// level is "debug", "info" (default), "warn" or "error". Both are validated
// by the config loader, so unknown values simply fall back to the defaults.
func New(w io.Writer, format, level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	if strings.EqualFold(format, "json") {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// Sub returns a logger tagged with the subsystem attribute that every
// component log line carries (docs/30_LOGGING_ERROR_HANDLING.md).
func Sub(l *slog.Logger, name string) *slog.Logger {
	return l.With("subsystem", name)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
