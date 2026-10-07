package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/statuspage"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/web/proxy"
	"github.com/drilonrecica/sinjal/web/templates"
)

// PageKeyLabel derives the key that signs status page cookies from the
// master key (vault.Key.Derive).
const PageKeyLabel = "sinjal status page v1"

// Password-protected pages (docs/12, docs/13 "Status page access").
const (
	pageCookie       = "sinjal_page"
	pageCookieTTL    = 7 * 24 * time.Hour
	pageMaxFailures  = 10
	pageWindow       = 15 * time.Minute
	pageTrackedKeys  = 4096
	pageFormMaxBody  = 16 << 10
	pagePasswordFail = "Incorrect password."
)

// pageAccess is where a page is being served: its base path, which is
// also the password form's action and the cookie's path.
type pageAccess struct {
	base   string // "/status/{slug}", "/s/{token}" or "/" on a mapped hostname
	mapped bool   // served at / of a mapped hostname, which has no sessions
	kind   int    // kindHTML (the page), kindJSON or kindFeed
}

// What a request for a page asks for. The same access rules decide all of
// them; only the refusal differs, because a machine reader has no use for
// a form or a redirect.
const (
	kindHTML = iota
	kindJSON
	kindFeed
)

// serve answers a request for page p: it enforces the page's visibility,
// then renders it. Nothing about the page is read for the response before
// access is granted, the cache included.
func (h *Public) serve(w http.ResponseWriter, r *http.Request, p store.StatusPageDetail, at pageAccess) {
	switch p.Visibility {
	case statuspage.Public, statuspage.Unlisted:
	case statuspage.Authenticated:
		if at.kind != kindHTML {
			if _, ok := SessionFromContext(r.Context()); !ok || at.mapped {
				refuse(w, "sign in to see this page")
				return
			}
			break
		}
		if at.mapped {
			h.signInElsewhere(w, r, p)
			return
		}
		if _, ok := SessionFromContext(r.Context()); !ok {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(at.base), http.StatusSeeOther)
			return
		}
	case statuspage.Password:
		if r.Method == http.MethodPost && at.kind == kindHTML {
			h.unlock(w, r, p, at)
			return
		}
		ok, err := h.unlocked(r, p)
		if err != nil {
			h.fail(w, r, "checking a page cookie", err)
			return
		}
		if !ok {
			if at.kind != kindHTML {
				refuse(w, "this page needs its password")
				return
			}
			h.passwordForm(w, r, p, at, http.StatusUnauthorized, "")
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if p.Visibility == statuspage.Unlisted {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		w.Header().Set("Referrer-Policy", "no-referrer")
	}
	switch at.kind {
	case kindJSON:
		h.writeJSON(w, r, p)
	case kindFeed:
		h.writeFeed(w, r, p, at)
	default:
		h.render(w, r, p)
	}
}

// refuse answers a machine reader that may not see a page: 401 and a
// plain line, never the page's content, a form or a redirect.
func refuse(w http.ResponseWriter, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, msg, http.StatusUnauthorized)
}

// signInElsewhere answers an authenticated page on a mapped hostname.
// Sessions belong to the instance's own host and /login is not served on
// mapped names, so the visitor is sent to the page on the base URL, where
// the normal sign-in returns to it; without a base URL there is nowhere
// to send them.
func (h *Public) signInElsewhere(w http.ResponseWriter, r *http.Request, p store.StatusPageDetail) {
	if h.baseURL == "" {
		render(w, r, h.log, http.StatusForbidden, templates.PublicMessagePage(p.Title,
			"Sign in to Sinjal to see this page. It is not available at this address.", p.Theme))
		return
	}
	http.Redirect(w, r, h.baseURL+"/status/"+p.Slug, http.StatusSeeOther)
}

// unlocked reports whether the request carries a valid cookie for p.
func (h *Public) unlocked(r *http.Request, p store.StatusPageDetail) (bool, error) {
	c, err := r.Cookie(pageCookie)
	if err != nil {
		return false, nil
	}
	hash, err := store.StatusPagePasswordHash(r.Context(), h.db.Reader, p.ID)
	if err != nil || hash == "" {
		return false, err
	}
	return validPageCookie(h.key, p.ID, hash, c.Value, h.now()), nil
}

// unlock checks a posted page password. Failures are counted per client
// address and page and checked before hashing, as for sign-in.
func (h *Public) unlock(w http.ResponseWriter, r *http.Request, p store.StatusPageDetail, at pageAccess) {
	r.Body = http.MaxBytesReader(w, r.Body, pageFormMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ip := proxy.ClientIP(r).String()
	key := ip + "\x00page\x00" + p.ID
	now := h.now()
	if h.limiter.Blocked(key, now) {
		h.log.Warn("status page password: rate limited", "client_ip", ip, "status_page_id", p.ID)
		w.Header().Set("Retry-After", "900")
		h.passwordForm(w, r, p, at, http.StatusTooManyRequests, "Too many failed attempts. Wait a few minutes and try again.")
		return
	}
	hash, err := store.StatusPagePasswordHash(r.Context(), h.db.Reader, p.ID)
	if err != nil {
		h.fail(w, r, "reading a page password", err)
		return
	}
	ok, _, err := auth.VerifyPassword(r.PostForm.Get("password"), hash)
	if err != nil && !errors.Is(err, auth.ErrInvalidHash) {
		h.fail(w, r, "checking a page password", err)
		return
	}
	if !ok {
		h.limiter.Add(key, now)
		h.log.Info("status page password refused", "client_ip", ip, "status_page_id", p.ID)
		h.passwordForm(w, r, p, at, http.StatusUnauthorized, pagePasswordFail)
		return
	}
	h.limiter.Reset(key)
	expires := now.Add(pageCookieTTL)
	http.SetCookie(w, &http.Cookie{
		Name: pageCookie, Value: pageCookieValue(h.key, p.ID, hash, expires), Path: at.base,
		Expires: expires, MaxAge: int(pageCookieTTL.Seconds()),
		HttpOnly: true, Secure: proxy.IsHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, at.base, http.StatusSeeOther)
}

func (h *Public) passwordForm(w http.ResponseWriter, r *http.Request, p store.StatusPageDetail, at pageAccess, status int, msg string) {
	render(w, r, h.log, status, templates.PublicPasswordPage(templates.PublicPasswordView{
		Title: p.Title, Action: at.base, Error: msg, CSRFToken: CSRFToken(r)}, p.Theme))
}

// pageCookieValue is "<expiry unix seconds>.<mac>". The MAC covers the
// page, the expiry and the stored password hash, so a cookie opens one
// page only, until it expires or the password changes.
func pageCookieValue(key []byte, pageID, passwordHash string, expires time.Time) string {
	exp := strconv.FormatInt(expires.Unix(), 10)
	return exp + "." + base64.RawURLEncoding.EncodeToString(pageMAC(key, pageID, passwordHash, exp))
}

func validPageCookie(key []byte, pageID, passwordHash, value string, now time.Time) bool {
	exp, mac, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	sec, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || !now.Before(time.Unix(sec, 0)) {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(mac)
	return err == nil && hmac.Equal(got, pageMAC(key, pageID, passwordHash, exp))
}

func pageMAC(key []byte, pageID, passwordHash, exp string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("page\x00" + pageID + "\x00" + exp + "\x00" + passwordHash))
	return m.Sum(nil)
}
