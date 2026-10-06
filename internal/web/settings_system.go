package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/audit"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/web/templates"
)

// SettingsSystem serves Settings → System. For now that is the read-only
// audit log; diagnostics join it later (docs/21_SYSTEM_DIAGNOSTICS.md).
// The log shows client addresses, so only admins read it.
type SettingsSystem struct {
	db  *db.DB
	log *slog.Logger
}

// NewSettingsSystem returns the Settings → System handler.
func NewSettingsSystem(d *db.DB, logger *slog.Logger) *SettingsSystem {
	return &SettingsSystem{db: d, log: logging.Sub(logger, "http")}
}

// RegisterSettingsSystem mounts the page inside RequireAdmin.
func RegisterSettingsSystem(r chi.Router, h *SettingsSystem) {
	r.Get("/settings/system", h.page)
	r.Head("/settings/system", h.page)
}

func (h *SettingsSystem) page(w http.ResponseWriter, r *http.Request) {
	// A bad or missing cursor means the first page.
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	entries, next, err := audit.List(r.Context(), h.db.Reader, before, audit.PageSize)
	if err != nil {
		h.log.Error("settings: reading the audit log failed", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	view := templates.SystemView{}
	for _, e := range entries {
		view.Audit = append(view.Audit, templates.AuditRow{
			When:    e.At.UTC().Format("2006-01-02 15:04:05"),
			Actor:   e.Actor,
			Event:   e.Type,
			Object:  strings.TrimSpace(e.ObjectType + " " + e.ObjectID),
			Details: auditDetails(e.Metadata),
		})
	}
	if next > 0 {
		view.OlderPath = fmt.Sprintf("/settings/system?before=%d", next)
	}
	render(w, r, h.log, http.StatusOK, templates.SettingsSystem(pageFor(r, "System — Sinjal"), view))
}

// auditDetails renders metadata as sorted "key=value" pairs.
func auditDetails(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return strings.Join(parts, " ")
}
