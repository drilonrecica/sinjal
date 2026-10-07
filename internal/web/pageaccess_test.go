package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/statuspage"
	"github.com/drilonrecica/sinjal/internal/store"
)

const pagePassword = "correct horse battery"

// passwordPage adds a password-protected page with one service.
func (e *appEnv) passwordPage(t *testing.T, slug string) string {
	t.Helper()
	m := e.addMonitor(t, "m-"+slug, "https://x.example.com/")
	hash, err := auth.HashPassword(pagePassword)
	if err != nil {
		t.Fatal(err)
	}
	return e.addPage(t, store.StatusPageInput{Slug: slug, Title: "Partners " + slug, Visibility: "password", PasswordHash: hash,
		Monitors: []store.StatusPageMonitorInput{{MonitorID: m, DisplayName: "Partner API"}}})
}

// unlock posts the page password as an anonymous visitor from the same
// origin and returns the response.
func (e *appEnv) unlock(path, password, ip string) *http.Response {
	r := req("POST", path, url.Values{"password": {password}})
	r.Header.Set("Origin", "http://example.com")
	if ip != "" {
		r.RemoteAddr = ip + ":1234"
	}
	return e.serve(r).Result()
}

func pageCookieOf(res *http.Response) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == pageCookie {
			return c
		}
	}
	return nil
}

func TestPasswordPage(t *testing.T) {
	e := newAppEnv(t)
	e.passwordPage(t, "partners")

	rec := e.serve(req("GET", "/status/partners", nil))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `type="password"`) || strings.Contains(rec.Body.String(), "Partner API") {
		t.Fatalf("locked page = %d\n%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the password form is cacheable")
	}

	res := e.unlock("/status/partners", "wrong password", "")
	if res.StatusCode != http.StatusUnauthorized || pageCookieOf(res) != nil {
		t.Fatalf("wrong password = %d, cookie %v", res.StatusCode, pageCookieOf(res))
	}

	res = e.unlock("/status/partners", pagePassword, "")
	c := pageCookieOf(res)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/status/partners" || c == nil {
		t.Fatalf("right password = %d %q, cookie %v", res.StatusCode, res.Header.Get("Location"), c)
	}
	if c.Path != "/status/partners" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure || c.MaxAge != 7*24*3600 {
		t.Errorf("cookie = %+v", c)
	}
	open := func(path string, c *http.Cookie) int {
		r := req("GET", path, nil)
		r.AddCookie(c)
		return e.serve(r).Code
	}
	if code := open("/status/partners", c); code != 200 {
		t.Fatalf("with the cookie = %d", code)
	}

	tampered := *c
	tampered.Value = c.Value[:len(c.Value)-2] + "xx"
	if code := open("/status/partners", &tampered); code != http.StatusUnauthorized {
		t.Errorf("tampered cookie = %d", code)
	}
	exp, _, _ := strings.Cut(c.Value, ".")
	longer := *c
	longer.Value = "9" + exp + c.Value[len(exp):]
	if code := open("/status/partners", &longer); code != http.StatusUnauthorized {
		t.Errorf("cookie with a moved expiry = %d", code)
	}

	// A cookie opens its own page only.
	e.passwordPage(t, "other")
	if code := open("/status/other", c); code != http.StatusUnauthorized {
		t.Errorf("cookie of another page = %d", code)
	}

	for _, line := range strings.Split(e.logs.String(), "\n") {
		if strings.Contains(line, pagePassword) || strings.Contains(line, "wrong password") {
			t.Fatalf("a password reached the log: %s", line)
		}
	}
	if !strings.Contains(e.logs.String(), "status page password refused") {
		t.Error("a refused password is not logged")
	}
}

func TestPasswordPageCookie(t *testing.T) {
	key := []byte("k")
	now := time.Now()
	v := pageCookieValue(key, "p1", "hash", now.Add(time.Hour))
	for _, c := range []struct {
		name              string
		key               []byte
		page, hash, value string
		at                time.Time
		want              bool
	}{
		{"valid", key, "p1", "hash", v, now, true},
		{"expired", key, "p1", "hash", v, now.Add(time.Hour), false},
		{"other page", key, "p2", "hash", v, now, false},
		{"password changed", key, "p1", "hash2", v, now, false},
		{"other key", []byte("x"), "p1", "hash", v, now, false},
		{"garbage", key, "p1", "hash", "abc", now, false},
		{"empty", key, "p1", "hash", "", now, false},
	} {
		if got := validPageCookie(c.key, c.page, c.hash, c.value, c.at); got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPasswordChangeLocksAgain(t *testing.T) {
	e := newAppEnv(t)
	id := e.passwordPage(t, "p")
	c := pageCookieOf(e.unlock("/status/p", pagePassword, ""))
	hash, _ := auth.HashPassword("a new password here")
	e.exec(t, `UPDATE status_pages SET password_hash = ? WHERE id = ?`, hash, id)
	r := req("GET", "/status/p", nil)
	r.AddCookie(c)
	if code := e.serve(r).Code; code != http.StatusUnauthorized {
		t.Errorf("cookie after a password change = %d", code)
	}
}

func TestPasswordRateLimit(t *testing.T) {
	e := newAppEnv(t)
	e.passwordPage(t, "p")
	for i := 0; i < pageMaxFailures; i++ {
		if res := e.unlock("/status/p", "nope", "192.0.2.1"); res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i+1, res.StatusCode)
		}
	}
	res := e.unlock("/status/p", pagePassword, "192.0.2.1")
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") != "900" || pageCookieOf(res) != nil {
		t.Fatalf("blocked = %d %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	// Another address is not blocked.
	if res := e.unlock("/status/p", pagePassword, "192.0.2.2"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("other address = %d", res.StatusCode)
	}
}

func TestPasswordPostNeedsSameOrigin(t *testing.T) {
	e := newAppEnv(t)
	e.passwordPage(t, "p")
	r := req("POST", "/status/p", url.Values{"password": {pagePassword}})
	r.Header.Set("Origin", "https://evil.example")
	if rec := e.serve(r); rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin post = %d", rec.Code)
	}
	// A signed-in visitor's form carries the CSRF token.
	e.addUser(t, "v1", "viewer", "viewer", "")
	if body := e.getAs(t, "v1", "GET", "/status/p").Body.String(); !strings.Contains(body, `name="_csrf"`) {
		t.Error("the form lacks the CSRF token for a signed-in visitor")
	}
}

func TestPostToOtherPages(t *testing.T) {
	e := newAppEnv(t)
	e.addPage(t, store.StatusPageInput{Slug: "pub", Title: "Pub"})
	r := req("POST", "/status/pub", url.Values{"password": {"x"}})
	r.Header.Set("Origin", "http://example.com")
	if rec := e.serve(r); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to a public page = %d", rec.Code)
	}
}

func TestAuthenticatedPage(t *testing.T) {
	e := newAppEnv(t)
	m := e.addMonitor(t, "m", "https://x.example.com/")
	e.addPage(t, store.StatusPageInput{Slug: "internal", Title: "Internal", Visibility: "authenticated",
		Monitors: []store.StatusPageMonitorInput{{MonitorID: m, DisplayName: "Intranet"}}})
	rec := e.serve(req("GET", "/status/internal", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?next=%2Fstatus%2Finternal" {
		t.Fatalf("anonymous = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// After signing in, login returns to the page.
	e.addUser(t, "v1", "vera", "viewer", "a long viewer password")
	r := req("POST", "/login", url.Values{"login": {"vera"}, "password": {"a long viewer password"}, "next": {"/status/internal"}})
	r.Header.Set("Origin", "http://example.com")
	if res := e.serve(r); res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/status/internal" {
		t.Fatalf("login = %d %q", res.Code, res.Header().Get("Location"))
	}
	for _, user := range []string{"v1"} {
		if rec := e.getAs(t, user, "GET", "/status/internal"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Intranet") {
			t.Errorf("%s = %d", user, rec.Code)
		}
	}
	e.addUser(t, "a1", "admin", "admin", "")
	if rec := e.getAs(t, "a1", "GET", "/status/internal"); rec.Code != 200 {
		t.Errorf("admin = %d", rec.Code)
	}
}

func TestUnlistedPage(t *testing.T) {
	e := newAppEnv(t)
	m := e.addMonitor(t, "m", "https://x.example.com/")
	token, hash, err := statuspage.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	id := e.addPage(t, store.StatusPageInput{Slug: "hidden", Title: "Hidden", Visibility: "unlisted", TokenHash: hash,
		Monitors: []store.StatusPageMonitorInput{{MonitorID: m, DisplayName: "Secret service"}}})

	rec := e.serve(req("GET", "/s/"+token, nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Secret service") {
		t.Fatalf("token address = %d", rec.Code)
	}
	if rec.Header().Get("X-Robots-Tag") != "noindex, nofollow" || rec.Header().Get("Referrer-Policy") != "no-referrer" ||
		!strings.Contains(rec.Body.String(), `<meta name="robots" content="noindex, nofollow">`) {
		t.Errorf("headers %v, robots meta missing or wrong", rec.Header())
	}
	if strings.Contains(e.logs.String(), token) {
		t.Error("the token reached the log")
	}
	other := "a"
	if token[10] == 'a' {
		other = "b"
	}
	for _, path := range []string{"/status/hidden", "/s/" + strings.ToUpper(token), "/s/abc", "/s/" + token[:10] + other + token[11:]} {
		if rec := e.serve(req("GET", path, nil)); rec.Code != 404 {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}

	// A new address stops the old one.
	newToken, newHash, _ := statuspage.NewToken()
	if err := store.SetStatusPageToken(t.Context(), e.db, id, newHash, time.Now()); err != nil {
		t.Fatal(err)
	}
	if rec := e.serve(req("GET", "/s/"+token, nil)); rec.Code != 404 {
		t.Errorf("old address = %d", rec.Code)
	}
	if rec := e.serve(req("GET", "/s/"+newToken, nil)); rec.Code != 200 {
		t.Errorf("new address = %d", rec.Code)
	}

	// Another page's token does not open a page that is not unlisted.
	pubToken, pubHash, _ := statuspage.NewToken()
	e.exec(t, `INSERT INTO status_pages (id, slug, title, visibility, unlisted_token_hash, created_at, updated_at)
		VALUES ('pub', 'pub', 'P', 'public', ?, 'now', 'now')`, pubHash)
	if rec := e.serve(req("GET", "/s/"+pubToken, nil)); rec.Code != 404 {
		t.Errorf("token of a public page = %d", rec.Code)
	}
}
