package web

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
)

const viewerPassword = "viewer passphrase 1"

func TestAdminCreatesViewer(t *testing.T) {
	e := newTOTPEnv(t)

	page := e.get("/settings/authentication").Body.String()
	for _, want := range []string{"Add a viewer", `action="/settings/authentication/viewers"`, "No viewers yet."} {
		if !strings.Contains(page, want) {
			t.Errorf("settings page lacks %q", want)
		}
	}

	rec := e.post("/settings/authentication/viewers", url.Values{"login": {"vera"}, "password": {viewerPassword}, "confirm": {viewerPassword}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings/authentication" {
		t.Fatalf("create = %d %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	page = e.get("/settings/authentication").Body.String()
	if !strings.Contains(page, "vera") || !strings.Contains(page, "Active") {
		t.Errorf("the new viewer is not listed:\n%s", page)
	}
	if strings.Contains(page, viewerPassword) {
		t.Error("the page shows the viewer's password")
	}

	// The new viewer signs in through the normal form and is read-only.
	rec = e.serve(req("POST", "/login", url.Values{"login": {"vera"}, "password": {viewerPassword}}))
	c := sessionCookie(rec)
	if rec.Code != 303 || c == nil {
		t.Fatalf("viewer sign-in = %d", rec.Code)
	}
	sess, user, err := e.sessions.Lookup(context.Background(), c.Value, time.Now())
	if err != nil || user.Role != "viewer" {
		t.Fatalf("lookup: %v, role %q", err, user.Role)
	}
	deny := e.serve(withCookie(req("POST", "/settings/authentication/viewers", url.Values{
		"login": {"mallory"}, "password": {viewerPassword}, "confirm": {viewerPassword}, CSRFFormField: {e.csrf.token(sess.ID)},
	}), c.Value))
	if deny.Code != 403 {
		t.Errorf("a viewer creating a viewer = %d, want 403", deny.Code)
	}
	for _, path := range []string{"/settings/authentication", "/settings/authentication/totp"} {
		if rec := e.serve(withCookie(req("GET", path, nil), c.Value)); rec.Code != 403 {
			t.Errorf("a viewer reading %s = %d, want 403", path, rec.Code)
		}
	}
}

func TestCreateViewerErrors(t *testing.T) {
	e := newTOTPEnv(t)
	e.addUser(t, "v1", "taken", "viewer", viewerPassword)
	for name, c := range map[string]struct {
		form url.Values
		want string
	}{
		"short password": {url.Values{"login": {"vera"}, "password": {"short"}, "confirm": {"short"}}, "Use at least"},
		"mismatch":       {url.Values{"login": {"vera"}, "password": {viewerPassword}, "confirm": {"other passphrase 2"}}, "The passwords do not match."},
		"bad login":      {url.Values{"login": {"two words"}, "password": {viewerPassword}, "confirm": {viewerPassword}}, `id="login-error"`},
		"taken":          {url.Values{"login": {"TAKEN"}, "password": {viewerPassword}, "confirm": {viewerPassword}}, "already taken"},
	} {
		rec := e.post("/settings/authentication/viewers", c.form)
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %d, want 422 containing %q", name, rec.Code, c.want)
		}
		if strings.Contains(rec.Body.String(), viewerPassword) {
			t.Errorf("%s: the form echoes the password", name)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM users`); n != 2 {
		t.Errorf("users = %d, want 2", n)
	}
}

func (e *appEnv) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.Reader.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAdminDisablesViewer(t *testing.T) {
	e := newTOTPEnv(t)
	e.addUser(t, "v1", "vera", "viewer", viewerPassword)
	vcookie, _ := e.signIn(t, "v1")
	signedIn := func() bool {
		return e.serve(withCookie(req("GET", "/monitors", nil), vcookie)).Code == 200
	}
	if !signedIn() {
		t.Fatal("the viewer cannot open a page")
	}

	if rec := e.post("/settings/authentication/viewers/v1/disable", url.Values{}); rec.Code != 303 {
		t.Fatalf("disable = %d", rec.Code)
	}
	if signedIn() {
		t.Error("a disabled viewer kept a working session")
	}
	if page := e.get("/settings/authentication").Body.String(); !strings.Contains(page, "Disabled") || !strings.Contains(page, "/viewers/v1/enable") {
		t.Errorf("the list does not show the disabled state:\n%s", page)
	}
	if rec := e.serve(req("POST", "/login", url.Values{"login": {"vera"}, "password": {viewerPassword}})); rec.Code != 401 {
		t.Errorf("a disabled viewer signed in: %d", rec.Code)
	}

	if rec := e.post("/settings/authentication/viewers/v1/enable", url.Values{}); rec.Code != 303 {
		t.Fatalf("enable = %d", rec.Code)
	}
	if rec := e.serve(req("POST", "/login", url.Values{"login": {"vera"}, "password": {viewerPassword}})); rec.Code != 303 {
		t.Errorf("an enabled viewer cannot sign in: %d", rec.Code)
	}

	// Admins cannot be disabled, and unknown ids are 404.
	for _, id := range []string{"u1", "nobody"} {
		if rec := e.post("/settings/authentication/viewers/"+id+"/disable", url.Values{}); rec.Code != 404 {
			t.Errorf("disable %q = %d, want 404", id, rec.Code)
		}
	}
}

// Viewer management needs a recent confirmation like the other credential
// changes.
func TestViewerManagementNeedsRecentAuth(t *testing.T) {
	e := newReauthEnv(t)
	e.age(t)
	f := url.Values{"login": {"vera"}, "password": {viewerPassword}, "confirm": {viewerPassword}, CSRFFormField: {e.csrf.token(e.sess.ID)}}
	rec := e.serve(withCookie(req("POST", "/settings/authentication/viewers", f), e.cookie))
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/reauth") {
		t.Errorf("stale create = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if n := e.count(t, `SELECT COUNT(*) FROM users WHERE role = 'viewer'`); n != 0 {
		t.Error("a viewer was created without re-authentication")
	}
}

func TestChangeOwnPassword(t *testing.T) {
	for _, role := range []string{"admin", "viewer"} {
		t.Run(role, func(t *testing.T) {
			e := newAppEnv(t)
			e.addUser(t, "u1", role, role, testPassword)
			cookie, sess := e.signIn(t, "u1")
			other, _ := e.signIn(t, "u1")
			post := func(f url.Values) *httptest.ResponseRecorder {
				f.Set(CSRFFormField, e.csrf.token(sess.ID))
				return e.serve(withCookie(req("POST", "/account/password", f), cookie))
			}

			if rec := e.serve(withCookie(req("GET", "/account/password", nil), cookie)); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Change password") {
				t.Fatalf("form = %d", rec.Code)
			}
			if rec := post(url.Values{"password": {"short"}, "confirm": {"short"}}); rec.Code != 422 {
				t.Errorf("short password = %d, want 422", rec.Code)
			}
			if rec := post(url.Values{"password": {"a brand new passphrase"}, "confirm": {"different passphrase"}}); rec.Code != 422 {
				t.Errorf("mismatch = %d, want 422", rec.Code)
			}

			rec := post(url.Values{"password": {"a brand new passphrase"}, "confirm": {"a brand new passphrase"}})
			if rec.Code != 303 || rec.Header().Get("Location") != "/account/password?changed=1" {
				t.Fatalf("change = %d %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body)
			}
			if sessionCookie(rec) == nil {
				t.Error("the session was not rotated")
			}
			if e.serve(withCookie(req("GET", "/monitors", nil), other)).Code == 200 {
				t.Error("another session survived the password change")
			}
			if rec := e.serve(req("POST", "/login", url.Values{"login": {role}, "password": {testPassword}})); rec.Code != 401 {
				t.Errorf("the old password still works: %d", rec.Code)
			}
			if rec := e.serve(req("POST", "/login", url.Values{"login": {role}, "password": {"a brand new passphrase"}})); rec.Code != 303 {
				t.Errorf("the new password does not work: %d", rec.Code)
			}
			if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'auth.password_changed' AND user_id = 'u1'`); n != 1 {
				t.Errorf("audit events = %d, want 1", n)
			}
		})
	}
}

func TestChangeOwnPasswordNeedsRecentAuth(t *testing.T) {
	e := newReauthEnv(t)
	e.age(t)
	rec := e.serve(withCookie(req("GET", "/account/password", nil), e.cookie))
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/reauth") {
		t.Errorf("stale GET = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	f := url.Values{"password": {"a brand new passphrase"}, "confirm": {"a brand new passphrase"}, CSRFFormField: {e.csrf.token(e.sess.ID)}}
	rec = e.serve(withCookie(req("POST", "/account/password", f), e.cookie))
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/reauth") {
		t.Errorf("stale POST = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	var hash string
	e.db.Reader.QueryRow(`SELECT password_hash FROM users WHERE id = 'u1'`).Scan(&hash)
	if ok, _, _ := auth.VerifyPassword(testPassword, hash); !ok {
		t.Error("the password changed without re-authentication")
	}
}

func TestSignOutOtherSessions(t *testing.T) {
	for _, role := range []string{"admin", "viewer"} {
		t.Run(role, func(t *testing.T) {
			e := newAppEnv(t)
			e.addUser(t, "u1", role, role, testPassword)
			e.addUser(t, "u2", "bystander", role, testPassword)
			cookie, sess := e.signIn(t, "u1")
			other1, _ := e.signIn(t, "u1")
			other2, _ := e.signIn(t, "u1")
			bystander, _ := e.signIn(t, "u2")
			alive := func(c string) bool { return e.serve(withCookie(req("GET", "/monitors", nil), c)).Code == 200 }

			f := url.Values{CSRFFormField: {e.csrf.token(sess.ID)}}
			rec := e.serve(withCookie(req("POST", "/account/sessions/sign-out-others", f), cookie))
			if rec.Code != 303 || rec.Header().Get("Location") != "/account/password?signedout=1" {
				t.Fatalf("sign out others = %d %q", rec.Code, rec.Header().Get("Location"))
			}
			if !alive(cookie) {
				t.Error("the current session was signed out")
			}
			if alive(other1) || alive(other2) {
				t.Error("another session of the user survived")
			}
			if !alive(bystander) {
				t.Error("another user's session was signed out")
			}
			var meta string
			if err := e.db.Reader.QueryRow(`SELECT metadata_json FROM audit_events WHERE event_type = 'auth.sessions_revoked' AND user_id = 'u1'`).Scan(&meta); err != nil || !strings.Contains(meta, `"count":"2"`) {
				t.Errorf("audit metadata = %q, %v; want count 2", meta, err)
			}

			page := e.serve(withCookie(req("GET", "/account/password?signedout=1", nil), cookie)).Body.String()
			if !strings.Contains(page, "Every other session was signed out.") {
				t.Error("the page does not confirm it")
			}
		})
	}
}

func TestSignOutOtherSessionsNeedsRecentAuth(t *testing.T) {
	e := newReauthEnv(t)
	other, _ := e.signIn(t, "u1")
	e.age(t)
	f := url.Values{CSRFFormField: {e.csrf.token(e.sess.ID)}}
	rec := e.serve(withCookie(req("POST", "/account/sessions/sign-out-others", f), e.cookie))
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/reauth") {
		t.Errorf("stale POST = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if e.serve(withCookie(req("GET", "/monitors", nil), other)).Code != 200 {
		t.Error("sessions were signed out without re-authentication")
	}
}

// The settings navigation links only to pages the role may open.
func TestSettingsNavigationByRole(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", testPassword)
	e.addUser(t, "v1", "vera", "viewer", testPassword)
	admin, _ := e.signIn(t, "a1")
	viewer, _ := e.signIn(t, "v1")

	body := e.serve(withCookie(req("GET", "/account/password", nil), admin)).Body.String()
	for _, want := range []string{`href="/settings/authentication"`, `href="/settings/system"`, `href="/account/password" aria-current="page"`} {
		if !strings.Contains(body, want) {
			t.Errorf("admin navigation lacks %q", want)
		}
	}
	body = e.serve(withCookie(req("GET", "/account/password", nil), viewer)).Body.String()
	if !strings.Contains(body, `aria-label="Settings"`) || !strings.Contains(body, `href="/account/password"`) {
		t.Error("the viewer has no account navigation")
	}
	for _, hidden := range []string{"/settings/authentication", "/settings/system"} {
		if strings.Contains(body, hidden) {
			t.Errorf("the viewer's page links to admin-only %s", hidden)
		}
	}
}
