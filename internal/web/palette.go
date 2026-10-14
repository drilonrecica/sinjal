package web

import (
	"net/http"
	"strings"

	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// paletteLimit is how many monitors the command palette lists for one
// search: enough to choose from, few enough to read.
const paletteLimit = 8

// palette serves GET /fragments/palette?q=: the command palette's monitor
// group (docs/03 "Command palette"), the monitors whose name, tag or
// public display label contains q (docs/29 "Search") and, for admins,
// whose target summary does: the scheme and host the column shows, never
// a path or query that may carry a secret. Matching ignores case; the
// first paletteLimit by name are listed. An empty q lists none.
func (h *Monitors) palette(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	admin := isAdmin(r)
	var found []templates.PaletteMonitor
	if q != "" {
		var err error
		if found, err = h.search(r, q, admin); err != nil {
			h.log.Error("palette search failed", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}
	render(w, r, h.log, http.StatusOK, templates.PaletteMonitors(found, admin))
}

// search matches q (lower case) in a fixed number of queries, however many
// monitors there are.
func (h *Monitors) search(r *http.Request, q string, admin bool) ([]templates.PaletteMonitor, error) {
	ctx, db := r.Context(), h.db.Reader
	monitors, err := store.ListMonitors(ctx, db)
	var tags, labels map[string][]string
	var targets map[string]string
	if err == nil {
		tags, err = store.TagsByMonitor(ctx, db)
	}
	if err == nil {
		labels, err = store.DisplayNamesByMonitor(ctx, db)
	}
	if err == nil && admin {
		targets, err = store.Targets(ctx, db)
	}
	if err != nil {
		return nil, err
	}
	var out []templates.PaletteMonitor
	for _, m := range monitors {
		if !paletteMatch(q, m, tags[m.ID], labels[m.ID], admin, targets[m.ID]) {
			continue
		}
		out = append(out, templates.PaletteMonitor{ID: m.ID, Name: m.Name, State: displayState(m), Paused: m.State == "paused"})
		if len(out) == paletteLimit {
			break
		}
	}
	return out, nil
}

func paletteMatch(q string, m store.Monitor, tags, labels []string, admin bool, target string) bool {
	if strings.Contains(strings.ToLower(m.Name), q) {
		return true
	}
	for _, list := range [][]string{tags, labels} {
		for _, s := range list {
			if strings.Contains(strings.ToLower(s), q) {
				return true
			}
		}
	}
	return admin && strings.Contains(strings.ToLower(targetSummary(m.Type, target)), q)
}
