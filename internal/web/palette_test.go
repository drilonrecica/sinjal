package web

import (
	"fmt"
	"strings"
	"testing"

	"github.com/drilonrecica/sinjal/internal/store"
)

func TestPaletteSearch(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	api := e.addMonitor(t, "Billing API", "https://billing.example.com/v1?token=s3cret", "payments")
	site := e.addMonitor(t, "Website", "https://www.example.org/")
	db := e.addMonitor(t, "Database", "https://db.internal/")
	e.addPage(t, store.StatusPageInput{Slug: "main", Title: "Status", Monitors: []store.StatusPageMonitorInput{{MonitorID: site, DisplayName: "Storefront"}}})
	e.exec(t, `UPDATE monitors SET current_state = 'paused', enabled = 0 WHERE id = ?`, db)

	search := func(user, q string) string {
		t.Helper()
		rec := e.getAs(t, user, "GET", "/fragments/palette?q="+q)
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("q=%q as %s = %d %v", q, user, rec.Code, rec.Header())
		}
		return rec.Body.String()
	}
	has := func(body, id string) bool { return strings.Contains(body, `id="palette-open-`+id+`"`) }

	for _, c := range []struct {
		user, q string
		want    []string
		not     []string
	}{
		{"a1", "BILLING", []string{api}, []string{site, db}}, // name, any case
		{"a1", "pay", []string{api}, []string{site}},         // tag
		{"a1", "storefront", []string{site}, []string{api}},  // public display label
		{"a1", "www.example", []string{site}, []string{api}}, // admin: target host
		{"a1", "s3cret", nil, []string{api}},                 // never the query
		{"a1", "v1", nil, []string{api}},                     // nor the path
		{"v1", "www.example", nil, []string{site}},           // viewers: no targets
		{"v1", "storefront", []string{site}, nil},
		{"a1", "", nil, []string{api, site, db}}, // nothing typed: nothing listed
	} {
		body := search(c.user, c.q)
		for _, id := range c.want {
			if !has(body, id) {
				t.Errorf("q=%q as %s: %s missing:\n%s", c.q, c.user, id, body)
			}
		}
		for _, id := range c.not {
			if has(body, id) {
				t.Errorf("q=%q as %s: %s listed:\n%s", c.q, c.user, id, body)
			}
		}
	}

	// Admins get Pause or Resume by the stored state; viewers neither.
	admin := search("a1", "a")
	for _, want := range []string{
		`formaction="/monitors/` + api + `/pause"`,
		`formaction="/monitors/` + db + `/resume"`,
	} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin results lack %s:\n%s", want, admin)
		}
	}
	if strings.Contains(admin, `/monitors/`+db+`/pause"`) || strings.Contains(admin, `/monitors/`+api+`/resume"`) {
		t.Errorf("admin results offer the wrong action:\n%s", admin)
	}
	if viewer := search("v1", "a"); strings.Contains(viewer, "formaction") || !has(viewer, api) {
		t.Errorf("viewer results:\n%s", viewer)
	}
}

func TestPaletteSearchLimit(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	for i := range paletteLimit + 3 {
		e.addMonitor(t, fmt.Sprintf("svc %02d", i), "https://example.com/")
	}
	body := e.getAs(t, "a1", "GET", "/fragments/palette?q=svc").Body.String()
	if n := strings.Count(body, `id="palette-open-`); n != paletteLimit {
		t.Errorf("%d monitors listed, want %d", n, paletteLimit)
	}
	if !strings.Contains(body, "svc 00") || strings.Contains(body, fmt.Sprintf("svc %02d", paletteLimit)) {
		t.Errorf("want the first by name:\n%s", body)
	}
}

func TestPaletteSearchRequiresSignIn(t *testing.T) {
	e := newAppEnv(t)
	if rec := e.serve(req("GET", "/fragments/palette?q=a", nil)); rec.Code == 200 {
		t.Errorf("anonymous = %d", rec.Code)
	}
}

// The shell carries the palette on every page: a combobox owning a
// listbox, navigation for everyone, Add monitor for admins only, and the
// three themes.
func TestShellPalette(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	admin := e.getAs(t, "a1", "GET", "/incidents").Body.String()
	for _, want := range []string{
		`<dialog class="palette" id="palette" aria-label="Command palette"`,
		`role="combobox" aria-expanded="true" aria-controls="palette-list"`,
		`id="palette-list" class="palette-list" role="listbox"`,
		`id="palette-nav-incidents" class="palette-option" role="option" tabindex="-1" href="/incidents"`,
		`href="/monitors/new"`,
		`data-palette-theme="stark"`, `data-palette-theme="terminal"`, `data-palette-theme="daylight"`,
		`data-palette-open hidden`,
		`name="_csrf"`,
	} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin shell lacks %s", want)
		}
	}
	viewer := e.getAs(t, "v1", "GET", "/incidents").Body.String()
	if !strings.Contains(viewer, `id="palette-nav-incidents"`) || strings.Contains(viewer, `id="palette-new-monitor"`) ||
		strings.Contains(viewer, `id="palette-nav-status-pages"`) {
		t.Error("viewer palette must have navigation but no admin commands")
	}
}
