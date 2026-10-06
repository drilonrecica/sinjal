package web

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/web/proxy"
)

// Passkey ceremonies (docs/13 "Passkeys"). A ceremony is two JSON requests:
// begin hands the browser a challenge, finish verifies the authenticator's
// answer. The state in between lives here, in memory, for a few minutes.
const (
	passkeyCeremonyTTL  = 5 * time.Minute
	passkeyCeremonyCap  = 256 // pending ceremonies kept; the oldest is dropped beyond that
	passkeyMaxBegin     = 4 << 10
	passkeyMaxFinish    = 64 << 10
	securePasskeyCookie = "__Host-sinjal_passkey"
	plainPasskeyCookie  = "sinjal_passkey"
)

// pendingCeremony is one begun ceremony: what the auth layer needs to
// verify the answer, and where to send the browser afterwards.
type pendingCeremony struct {
	ceremony auth.PasskeyCeremony
	next     string
	expires  time.Time
}

// ceremonyStore keeps begun ceremonies until they are finished. Entries are
// single-use: take removes them, so a captured answer cannot be replayed.
// The map is bounded, so anonymous begin requests cannot grow memory.
// Losing it on restart only means starting a ceremony again.
type ceremonyStore struct {
	mu      sync.Mutex
	entries map[string]pendingCeremony
}

func newCeremonyStore() *ceremonyStore {
	return &ceremonyStore{entries: make(map[string]pendingCeremony, passkeyCeremonyCap)}
}

// put stores p and returns its unguessable ID for the cookie.
func (s *ceremonyStore) put(p pendingCeremony, now time.Time) string {
	p.expires = now.Add(passkeyCeremonyTTL)
	id := rand.Text()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) >= passkeyCeremonyCap {
		var oldest string
		for k, e := range s.entries {
			if !now.Before(e.expires) {
				delete(s.entries, k)
			} else if oldest == "" || e.expires.Before(s.entries[oldest].expires) {
				oldest = k
			}
		}
		if len(s.entries) >= passkeyCeremonyCap {
			delete(s.entries, oldest)
		}
	}
	s.entries[id] = p
	return id
}

// take returns and removes the ceremony, if it exists and has not expired.
func (s *ceremonyStore) take(id string, now time.Time) (pendingCeremony, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.entries[id]
	delete(s.entries, id)
	return p, ok && now.Before(p.expires)
}

// Passkeys serves the WebAuthn ceremonies for sign-in, re-authentication
// and registration. Sign-in and re-authentication failures share the login
// limiter.
type Passkeys struct {
	passkeys *auth.Passkeys
	sessions *auth.Sessions
	login    *Login
	store    *ceremonyStore
	log      *slog.Logger
	now      func() time.Time
}

// NewPasskeys returns the ceremony handler.
func NewPasskeys(p *auth.Passkeys, sessions *auth.Sessions, login *Login, logger *slog.Logger) *Passkeys {
	return &Passkeys{passkeys: p, sessions: sessions, login: login, store: newCeremonyStore(), log: logging.Sub(logger, "auth"), now: time.Now}
}

// RegisterPasskeyLogin mounts the public sign-in ceremony in the session group.
func RegisterPasskeyLogin(r chi.Router, h *Passkeys) {
	r.Post("/login/passkey/begin", h.loginBegin)
	r.Post("/login/passkey/finish", h.loginFinish)
}

// RegisterPasskeyReauth mounts the re-authentication ceremony inside RequireAuth.
func RegisterPasskeyReauth(r chi.Router, h *Passkeys) {
	r.Post("/reauth/passkey/begin", h.reauthBegin)
	r.Post("/reauth/passkey/finish", h.reauthFinish)
}

// RegisterPasskeyRegistration mounts adding a passkey inside RequireAdmin,
// behind recent (RequireRecentAuth).
func RegisterPasskeyRegistration(r chi.Router, h *Passkeys, recent func(http.Handler) http.Handler) {
	r.With(recent).Post(settingsAuthPath+"/passkeys/begin", h.registerBegin)
	r.With(recent).Post(settingsAuthPath+"/passkeys/finish", h.registerFinish)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func passkeyCookieName(r *http.Request) string {
	if proxy.IsHTTPS(r) {
		return securePasskeyCookie
	}
	return plainPasskeyCookie
}

func setPasskeyCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     passkeyCookieName(r),
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   proxy.IsHTTPS(r),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// beginRequest is the optional JSON body of a begin request.
type beginRequest struct {
	Next  string `json:"next"`
	Label string `json:"label"`
}

// begin runs the checks every ceremony starts with and decodes the request.
// It answers the request itself and reports false when the ceremony cannot
// start. A wrong address is the usual reverse-proxy mistake, so the message
// names both sides.
func (h *Passkeys) begin(w http.ResponseWriter, r *http.Request) (beginRequest, bool) {
	var req beginRequest
	if h.passkeys.Unavailable() != "" {
		jsonError(w, http.StatusNotFound, "Passkeys are not available on this server.")
		return req, false
	}
	if got, want := requestOrigin(r), h.passkeys.Origin(); !strings.EqualFold(got, want) {
		h.log.Warn("passkey ceremony from an address other than SINJAL_BASE_URL", "request_origin", got, "expected_origin", want)
		jsonError(w, http.StatusBadRequest, "Passkeys only work at "+want+", but this page was opened at "+got+
			". Open Sinjal at that address. If it already is, check SINJAL_BASE_URL and SINJAL_TRUSTED_PROXIES on the server.")
		return req, false
	}
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, passkeyMaxBegin)).Decode(&req)
	if err != nil && !errors.Is(err, io.EOF) {
		jsonError(w, http.StatusBadRequest, "The request could not be read.")
		return req, false
	}
	return req, true
}

// started stores the ceremony, sets its cookie and sends the options.
func (h *Passkeys) started(w http.ResponseWriter, r *http.Request, options any, c auth.PasskeyCeremony, next string) {
	body, err := auth.OptionsJSON(options)
	if err != nil {
		h.fail(w, "encoding the options", err)
		return
	}
	id := h.store.put(pendingCeremony{ceremony: c, next: safeNextOrEmpty(next)}, h.now())
	setPasskeyCookie(w, r, id, int(passkeyCeremonyTTL.Seconds()))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(body)
}

// pending returns the ceremony the request's cookie names and ends it. It
// answers the request itself and reports false when there is none.
func (h *Passkeys) pending(w http.ResponseWriter, r *http.Request) (pendingCeremony, bool) {
	var p pendingCeremony
	ok := false
	if c, err := r.Cookie(passkeyCookieName(r)); err == nil {
		p, ok = h.store.take(c.Value, h.now())
		setPasskeyCookie(w, r, "", -1)
	}
	if !ok {
		jsonError(w, http.StatusBadRequest, "The passkey request expired. Try again.")
	}
	r.Body = http.MaxBytesReader(w, r.Body, passkeyMaxFinish)
	return p, ok
}

func (h *Passkeys) fail(w http.ResponseWriter, what string, err error) {
	h.log.Error("passkey: "+what+" failed", "error", err)
	jsonError(w, http.StatusInternalServerError, "Something went wrong. Check the server log and try again.")
}

// limited answers 429 when key has used up the failure allowance.
func (h *Passkeys) limited(w http.ResponseWriter, key, ip string, now time.Time) bool {
	if !h.login.limiter.Blocked(key, now) {
		return false
	}
	h.log.Warn("passkey: rate limited", "client_ip", ip)
	w.Header().Set("Retry-After", "900")
	jsonError(w, http.StatusTooManyRequests, "Too many failed attempts. Wait a few minutes and try again.")
	return true
}

// passkeyLoginKey counts failed passkey sign-ins per client address (no
// account is known until one succeeds). The two NULs keep it apart from
// password keys (ip NUL login) and it is not a user ID (re-auth keys).
func passkeyLoginKey(ip string) string { return ip + "\x00\x00passkey" }

func (h *Passkeys) loginBegin(w http.ResponseWriter, r *http.Request) {
	req, ok := h.begin(w, r)
	if !ok {
		return
	}
	ip := proxy.ClientIP(r).String()
	if h.limited(w, passkeyLoginKey(ip), ip, h.now()) {
		return
	}
	options, c, err := h.passkeys.BeginLogin()
	if err != nil {
		h.fail(w, "beginning a sign-in", err)
		return
	}
	h.started(w, r, options, c, req.Next)
}

func (h *Passkeys) loginFinish(w http.ResponseWriter, r *http.Request) {
	p, ok := h.pending(w, r)
	if !ok {
		return
	}
	ip := proxy.ClientIP(r).String()
	key := passkeyLoginKey(ip)
	now := h.now()
	if h.limited(w, key, ip, now) {
		return
	}
	user, err := h.passkeys.FinishLogin(r.Context(), p.ceremony, r.Body, ip, now)
	switch {
	case errors.Is(err, auth.ErrPasskeyRejected):
		h.login.limiter.Add(key, now)
		h.log.Info("passkey sign-in failed", "client_ip", ip)
		jsonError(w, http.StatusUnauthorized, "That passkey was not accepted.")
		return
	case err != nil:
		h.fail(w, "finishing a sign-in", err)
		return
	}
	h.login.limiter.Reset(key)
	if err := h.login.startSession(w, r, user, now); err != nil {
		jsonError(w, http.StatusInternalServerError, "Signing in failed. Check the server log and try again.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": safeNext(p.next)})
}

func (h *Passkeys) reauthBegin(w http.ResponseWriter, r *http.Request) {
	req, ok := h.begin(w, r)
	if !ok {
		return
	}
	cs, _ := SessionFromContext(r.Context())
	ip := proxy.ClientIP(r).String()
	if h.limited(w, reauthLimitKey(ip, cs.User.ID), ip, h.now()) {
		return
	}
	options, c, err := h.passkeys.BeginReauth(r.Context(), cs.User.ID)
	switch {
	case errors.Is(err, auth.ErrNoPasskeys):
		jsonError(w, http.StatusBadRequest, "This account has no passkey. Use your password.")
	case err != nil:
		h.fail(w, "beginning a re-authentication", err)
	default:
		h.started(w, r, options, c, req.Next)
	}
}

func (h *Passkeys) reauthFinish(w http.ResponseWriter, r *http.Request) {
	p, ok := h.pending(w, r)
	if !ok {
		return
	}
	cs, _ := SessionFromContext(r.Context())
	ip := proxy.ClientIP(r).String()
	key := reauthLimitKey(ip, cs.User.ID)
	now := h.now()
	if h.limited(w, key, ip, now) {
		return
	}
	err := h.passkeys.FinishReauth(r.Context(), p.ceremony, cs.User.ID, r.Body, ip, now)
	switch {
	case errors.Is(err, auth.ErrPasskeyRejected):
		h.login.limiter.Add(key, now)
		h.log.Info("passkey reauth failed", "user_id", cs.User.ID, "client_ip", ip)
		jsonError(w, http.StatusUnauthorized, "That passkey was not accepted.")
		return
	case err != nil:
		h.fail(w, "finishing a re-authentication", err)
		return
	}
	h.login.limiter.Reset(key)
	sess, err := rotateSession(w, r, h.sessions, now)
	if err != nil {
		h.fail(w, "rotating the session", err)
		return
	}
	h.log.Info("reauthenticated", "user_id", cs.User.ID, "session_id", sess.ID, "factor", "passkey")
	writeJSON(w, http.StatusOK, map[string]string{"redirect": safeNext(p.next)})
}

func (h *Passkeys) registerBegin(w http.ResponseWriter, r *http.Request) {
	req, ok := h.begin(w, r)
	if !ok {
		return
	}
	cs, _ := SessionFromContext(r.Context())
	options, c, err := h.passkeys.BeginRegistration(r.Context(), cs.User.ID, req.Label)
	var in *auth.InputError
	switch {
	case errors.As(err, &in):
		jsonError(w, http.StatusUnprocessableEntity, in.Message)
	case err != nil:
		h.fail(w, "beginning a registration", err)
	default:
		h.started(w, r, options, c, "")
	}
}

func (h *Passkeys) registerFinish(w http.ResponseWriter, r *http.Request) {
	p, ok := h.pending(w, r)
	if !ok {
		return
	}
	cs, _ := SessionFromContext(r.Context())
	now := h.now()
	k, err := h.passkeys.FinishRegistration(r.Context(), p.ceremony, cs.User.ID, r.Body, cs.Session.ID, proxy.ClientIP(r).String(), now)
	switch {
	case errors.Is(err, auth.ErrPasskeyRejected):
		jsonError(w, http.StatusBadRequest, "The passkey could not be added. Try again.")
		return
	case err != nil:
		h.fail(w, "finishing a registration", err)
		return
	}
	h.log.Info("passkey added", "user_id", cs.User.ID, "passkey_id", k.ID)
	// The other sessions are already deleted; the current one is rotated.
	if _, err := rotateSession(w, r, h.sessions, now); err != nil {
		h.log.Error("rotating the session after adding a passkey failed", "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": settingsAuthPath})
}
