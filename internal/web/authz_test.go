package web

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Routes reachable without a session, for any method.
var publicRoutes = map[string]bool{
	"/healthz":  true,
	"/readyz":   true,
	"/static/*": true,
	"/setup":    true,
	"/login":    true,
	"/logout":   true, // ends the caller's own session; harmless without one
}

// State-changing routes a viewer may use: they act only on the caller's own
// session or account.
var viewerMutations = map[string]bool{
	"/reauth": true, // confirms the caller's own password
}

var routeParamRe = regexp.MustCompile(`\{[^}]+\}|\*`)

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// guardViolations walks every registered route and probes it:
//   - non-public routes: anonymous GET/HEAD → 303 to /login, anything else → 401;
//   - non-public state-changing routes outside viewerMutations: a viewer with
//     a valid CSRF token → 403.
//
// A route mounted outside the RequireAuth/RequireAdmin groups is reported.
func guardViolations(t *testing.T, e *appEnv, router chi.Routes) []string {
	t.Helper()
	e.addUser(t, "viewer", "viewer", "viewer", "")
	cookie, sess := e.signIn(t, "viewer")
	token := e.csrf.token(sess.ID)

	var bad []string
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if publicRoutes[route] {
			return nil
		}
		path := routeParamRe.ReplaceAllString(route, "x")

		rec := e.serve(req(method, path, url.Values{}))
		if isSafeMethod(method) {
			if loc := rec.Header().Get("Location"); method != http.MethodOptions && (rec.Code != 303 || !strings.HasPrefix(loc, "/login")) {
				bad = append(bad, fmt.Sprintf("%s %s anonymous = %d %q, want 303 to /login", method, route, rec.Code, loc))
			}
			return nil
		}
		if rec.Code != 401 {
			bad = append(bad, fmt.Sprintf("%s %s anonymous = %d, want 401", method, route, rec.Code))
		}
		if viewerMutations[route] {
			return nil
		}
		r := withCookie(req(method, path, url.Values{CSRFFormField: {token}}), cookie)
		if rec := e.serve(r); rec.Code != 403 {
			bad = append(bad, fmt.Sprintf("%s %s as viewer = %d, want 403", method, route, rec.Code))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return bad
}

// TestRouteTableGuards checks the production route table: no route can be
// added later without the right guard going unnoticed.
func TestRouteTableGuards(t *testing.T) {
	e := newAppEnv(t)
	for _, v := range guardViolations(t, e, e.h.(chi.Routes)) {
		t.Error(v)
	}
}

// TestRouteTableGuardsCatchOmissions mounts routes outside the guards, as a
// careless future change would, and expects the check to report them.
func TestRouteTableGuardsCatchOmissions(t *testing.T) {
	e := newAppEnv(t)
	r := e.h.(*chi.Mux)
	ok := func(w http.ResponseWriter, _ *http.Request) {}
	logger, _ := quietLogger()
	r.Post("/monitors/{id}/delete", ok) // state change without RequireAdmin
	r.Get("/secret-page", ok)           // page without RequireAuth
	r.With(LoadSession(e.sessions, logger), RequireAuth(logger)).Post("/viewer-can-do-this", ok)

	got := strings.Join(guardViolations(t, e, r), "\n")
	for _, want := range []string{
		"POST /monitors/{id}/delete anonymous = 200",
		"POST /monitors/{id}/delete as viewer = 200",
		"GET /secret-page anonymous = 200",
		"POST /viewer-can-do-this as viewer = 200",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("guard check did not report %q; got:\n%s", want, got)
		}
	}
}

func TestRequireAuth(t *testing.T) {
	e := newAppEnv(t)
	rec := e.serve(req("GET", "/monitors?filter=down", nil))
	if rec.Code != 303 || rec.Header().Get("Location") != "/login?next=%2Fmonitors%3Ffilter%3Ddown" {
		t.Errorf("anonymous page = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := e.serve(req("GET", "/", nil)); rec.Header().Get("Location") != "/login" {
		t.Errorf("anonymous / → %q, want /login without next", rec.Header().Get("Location"))
	}
	r := req("GET", "/monitors", nil)
	r.Header.Set("HX-Request", "true")
	if rec := e.serve(r); rec.Code != 401 || rec.Header().Get("HX-Redirect") != "/login?next=%2Fmonitors" {
		t.Errorf("anonymous htmx = %d %q", rec.Code, rec.Header().Get("HX-Redirect"))
	}

	// Both roles can read; the page uses the user's theme preference.
	e.addUser(t, "v1", "viewer", "viewer", "")
	if _, err := e.db.Writer.Exec(`UPDATE users SET theme = 'paper', density = 'compact' WHERE id = 'v1'`); err != nil {
		t.Fatal(err)
	}
	cookie, _ := e.signIn(t, "v1")
	rec = e.serve(withCookie(req("GET", "/monitors", nil), cookie))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `data-theme="paper" data-density="compact"`) {
		t.Errorf("viewer page = %d, theme not applied", rec.Code)
	}
}

func TestRequireAdmin(t *testing.T) {
	e := newAppEnv(t)
	logger, _ := quietLogger()
	e.h.(*chi.Mux).With(LoadSession(e.sessions, logger), RequireAdmin(logger)).
		Post("/probe", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	admin, _ := e.signIn(t, "a1")
	viewer, _ := e.signIn(t, "v1")

	for _, tc := range []struct {
		name, cookie string
		want         int
	}{
		{"anonymous", "", 401},
		{"viewer", viewer, 403},
		{"admin", admin, 204},
	} {
		r := req("POST", "/probe", url.Values{})
		if tc.cookie != "" {
			r = withCookie(r, tc.cookie)
		}
		rec := e.serve(r)
		if rec.Code != tc.want {
			t.Errorf("%s: POST = %d, want %d", tc.name, rec.Code, tc.want)
		}
		if tc.want == 403 && !strings.Contains(rec.Body.String(), "can view Sinjal but not change it") {
			t.Errorf("403 body: %s", rec.Body.String())
		}
	}
}
