package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestAppearanceSavesOwnPreferences(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")

	page := e.getAs(t, "v1", "GET", "/account/appearance")
	if page.Code != 200 || !strings.Contains(page.Body.String(), `data-prefs="user"`) {
		t.Fatalf("GET = %d, want a signed-in page carrying data-prefs", page.Code)
	}

	rec := e.postAs(t, "v1", "/account/appearance", url.Values{"theme": {"terminal"}, "density": {"compact"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/account/appearance?saved=1" {
		t.Fatalf("POST = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// No "ambient" field: the checkbox is off.
	body := e.getAs(t, "v1", "GET", "/monitors").Body.String()
	if !strings.Contains(body, `data-theme="terminal" data-density="compact" data-ambient="off"`) {
		t.Errorf("saved preferences not applied: %s", body[:min(len(body), 300)])
	}

	rec = e.postAs(t, "v1", "/account/appearance", url.Values{"theme": {"neon"}, "density": {"compact"}})
	if rec.Code != 422 {
		t.Errorf("unknown theme = %d, want 422", rec.Code)
	}
	var theme string
	if err := e.db.Reader.QueryRow(`SELECT theme FROM users WHERE id = 'v1'`).Scan(&theme); err != nil || theme != "terminal" {
		t.Errorf("stored theme = %q, %v; a refused value must not be stored", theme, err)
	}
}

func TestPrehydrationScriptIsOnTheLayoutOnly(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	body := e.getAs(t, "v1", "GET", "/monitors").Body.String()
	if strings.Count(body, "<script>") != 1 || !strings.Contains(body, `sinjal.prefs`) {
		t.Errorf("signed-in page must carry exactly the pre-hydration inline script")
	}
}
