package web

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/audit"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// The heartbeat push URL is shown once: in the response to the create
// that issued its token, or to a regeneration. Only the token's hash is
// stored, so no later page can show it; the response is no-store like
// every admin page, and the token is never logged.

// showToken renders the one-time page with the push URL of monitor id.
func (h *Monitors) showToken(w http.ResponseWriter, r *http.Request, id, name, token string, regenerated bool) {
	origin := requestOrigin(r)
	render(w, r, h.log, http.StatusOK, templates.HeartbeatTokenPage(pageFor(r, "Push URL — Sinjal"), templates.HeartbeatToken{
		MonitorID:   id,
		Name:        name,
		URL:         origin + "/api/v1/heartbeat/" + token,
		BearerURL:   origin + "/api/v1/heartbeat",
		Token:       token,
		Regenerated: regenerated,
	}))
}

// regenerateToken serves POST /monitors/{id}/heartbeat/token: a new token
// for a heartbeat monitor, the old one stops working at once. It sits
// behind RequireRecentAuth (docs/13: reveal or regenerate a sensitive
// token).
func (h *Monitors) regenerateToken(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), chi.URLParam(r, "id")
	m, err := store.GetMonitor(ctx, h.db.Reader, id)
	if err == nil && m.Type != store.TypeHeartbeat {
		err = store.ErrNotFound
	}
	var token string
	if err == nil {
		token, err = store.SetHeartbeatToken(ctx, h.db, id)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "regenerating a heartbeat token", err)
		return
	}
	cs, _ := SessionFromContext(ctx)
	h.log.Info("heartbeat token regenerated", "user_id", cs.User.ID, "monitor_id", id)
	h.audit(r, audit.MonitorTokenRegenerated, id, m.Name)
	h.showToken(w, r, id, m.Name, token, true)
}
