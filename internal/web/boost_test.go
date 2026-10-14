package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBoostToPane(t *testing.T) {
	h := boostToPane(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest("GET", "/incidents", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Header().Get("HX-Retarget") != "" {
		t.Fatal("a plain request is retargeted")
	}
	r.Header.Set("HX-Boosted", "true")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	for k, v := range map[string]string{"HX-Retarget": "#main", "HX-Reselect": "#main", "HX-Reswap": "outerHTML", "Vary": "HX-Boosted"} {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// Through the real router: a boosted page view comes back aimed at the
// pane; a live fragment's own refresh (not boosted) does not.
func TestBoostedNavigationTargetsThePane(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	cookie, _ := e.signIn(t, "v1")
	r := withCookie(req("GET", "/incidents", nil), cookie)
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Boosted", "true")
	if rec := e.serve(r); rec.Header().Get("HX-Retarget") != "#main" {
		t.Errorf("boosted page: %v", rec.Header())
	}
	f := withCookie(req("GET", "/fragments/monitors", nil), cookie)
	f.Header.Set("HX-Request", "true")
	if rec := e.serve(f); rec.Header().Get("HX-Retarget") != "" {
		t.Error("a fragment refresh is retargeted")
	}
}
