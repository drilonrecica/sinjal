package web

import (
	"crypto/sha256"
	"encoding/base32"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/store"
)

// pageForm is a valid form for a public page; monitors are added with show.
func pageForm(title, slug string) url.Values {
	return url.Values{"title": {title}, "slug": {slug}, "visibility": {"public"}, "theme": {"paper"}, "incident_days": {"30"}, "show_powered_by": {"1"}}
}

// show adds a monitor to the form under a public name.
func show(f url.Values, id, name, group, order string) {
	f.Set("m_show_"+id, "1")
	f.Set("m_name_"+id, name)
	f.Set("m_group_"+id, group)
	f.Set("m_order_"+id, order)
}

func (e *appEnv) pageID(t *testing.T, slug string) string {
	t.Helper()
	var id string
	if err := e.db.Reader.QueryRow(`SELECT id FROM status_pages WHERE slug = ?`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

var secretAddress = regexp.MustCompile(`<code id="page-url">https?://[^/<]+/s/([a-z2-7]{26})</code>`)

func TestStatusPagesAreAdminOnly(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	e.postAs(t, "a1", "/status-pages", pageForm("Main", "main"))
	id := e.pageID(t, "main")

	if rec := e.serve(req("GET", "/status-pages", nil)); rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous list = %d, want a redirect to login", rec.Code)
	}
	for _, p := range []struct{ method, path string }{
		{"GET", "/status-pages"}, {"GET", "/status-pages/new"}, {"GET", "/status-pages/" + id + "/edit"},
	} {
		if rec := e.getAs(t, "v1", p.method, p.path); rec.Code != http.StatusForbidden {
			t.Errorf("viewer %s %s = %d, want 403", p.method, p.path, rec.Code)
		}
	}
	for _, p := range []string{"/status-pages", "/status-pages/" + id, "/status-pages/" + id + "/delete", "/status-pages/" + id + "/token"} {
		if rec := e.postAs(t, "v1", p, pageForm("X", "x")); rec.Code != http.StatusForbidden {
			t.Errorf("viewer POST %s = %d, want 403", p, rec.Code)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM status_pages`); n != 1 {
		t.Errorf("pages = %d: a viewer changed something", n)
	}
	// A state change needs the CSRF token too.
	cookie, _ := e.signIn(t, "a1")
	if rec := e.serve(withCookie(req("POST", "/status-pages", pageForm("No csrf", "nocsrf")), cookie)); rec.Code != http.StatusForbidden {
		t.Errorf("POST without a CSRF token = %d, want 403", rec.Code)
	}
}

func TestStatusPageList(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")

	body := e.getAs(t, "a1", "GET", "/status-pages").Body.String()
	if !strings.Contains(body, "No status pages") || !strings.Contains(body, `href="/status-pages/new"`) {
		t.Errorf("empty list:\n%s", body)
	}
	if rec := e.getAs(t, "a1", "HEAD", "/status-pages"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d bytes", rec.Code, rec.Body.Len())
	}

	api := e.addMonitor(t, "API", "https://api.example.com")
	f := pageForm("Public status", "status")
	f.Set("hosts", "Status.Example.com")
	show(f, api, "Our API", "", "")
	e.postAs(t, "a1", "/status-pages", f)
	u := pageForm("Partners", "partners")
	u.Set("visibility", "unlisted")
	e.postAs(t, "a1", "/status-pages", u)

	body = e.getAs(t, "a1", "GET", "/status-pages").Body.String()
	for _, want := range []string{"Public status", `href="/status/status"`, "1 monitor", "status.example.com", "Partners", "Unlisted", "Secret address, shown once"} {
		if !strings.Contains(body, want) {
			t.Errorf("list lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "/status/partners") {
		t.Error("an unlisted page is listed with a /status address")
	}
}

func TestStatusPageCreateEditDelete(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	api := e.addMonitor(t, "API", "https://api.internal.example.com/health")
	site := e.addMonitor(t, "Site", "https://example.com")

	if rec := e.getAs(t, "a1", "GET", "/status-pages/new"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="slug"`) ||
		!strings.Contains(rec.Body.String(), `name="m_name_`+api+`"`) {
		t.Fatalf("new form = %d", rec.Code)
	}
	f := pageForm("  Acme status ", "Acme")
	f.Set("description", "How Acme is doing")
	f.Set("accent", "#3E67A8")
	f.Set("groups", "Web\nData\n")
	f.Set("hosts", "status.acme.test\nStatus.Acme.test.,other.acme.test")
	f.Set("incident_days", "14")
	f.Del("show_powered_by")
	show(f, api, "Public API", "Web", "2")
	show(f, site, "Website", "web", "1") // a group matches without regard to case
	f.Set("m_latency_"+site, "1")
	rec := e.postAs(t, "a1", "/status-pages", f)
	if rec.Code != 303 || rec.Header().Get("Location") != "/status-pages" {
		t.Fatalf("create = %d %s\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	id := e.pageID(t, "acme")
	p, err := store.GetStatusPage(t.Context(), e.db.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Acme status" || p.Description != "How Acme is doing" || p.Accent != "#3e67a8" || p.Visibility != "public" ||
		p.Theme != "paper" || p.IncidentDays != 14 || p.ShowPoweredBy || p.HasPassword || p.HasToken {
		t.Errorf("page = %+v", p.StatusPage)
	}
	if len(p.Groups) != 2 || p.Groups[0].Name != "Web" || p.Groups[1].Name != "Data" {
		t.Errorf("groups = %+v", p.Groups)
	}
	if len(p.Monitors) != 2 || p.Monitors[0].MonitorID != site || p.Monitors[0].DisplayName != "Website" || !p.Monitors[0].ShowLatency ||
		p.Monitors[1].MonitorID != api || p.Monitors[1].DisplayName != "Public API" || p.Monitors[1].ShowLatency {
		t.Errorf("monitors = %+v, want the site (position 1) then the API", p.Monitors)
	}
	if len(p.Hosts) != 2 || p.Hosts[0] != "other.acme.test" || p.Hosts[1] != "status.acme.test" {
		t.Errorf("hosts = %v, want normalized and deduplicated", p.Hosts)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'status_page.created' AND object_id = ?`, id); n != 1 {
		t.Errorf("%d created audit events", n)
	}

	// The edit form shows what is stored.
	edit := e.getAs(t, "a1", "GET", "/status-pages/"+id+"/edit").Body.String()
	for _, want := range []string{`value="Acme status"`, `value="acme"`, `value="#3e67a8"`, `value="14"`, `value="Public API"`, `value="Website"`,
		"status.acme.test", `<option value="Web" selected>`, "Delete this page"} {
		if !strings.Contains(edit, want) {
			t.Errorf("edit form lacks %q", want)
		}
	}
	if strings.Contains(edit, "Issue a new address") {
		t.Error("a public page offers a new secret address")
	}
	if strings.Index(edit, "Show <span") > strings.Index(edit, "Public API") && strings.Index(edit, `name="m_name_`+site+`"`) > strings.Index(edit, `name="m_name_`+api+`"`) {
		t.Error("monitors are not in position order")
	}

	// Edit: rename, drop one monitor and a group, change the theme.
	g := pageForm("Acme", "acme")
	g.Set("theme", "carbon")
	g.Set("accent", "")
	g.Set("groups", "Web")
	show(g, site, "Website", "Web", "0")
	if rec := e.postAs(t, "a1", "/status-pages/"+id, g); rec.Code != 303 {
		t.Fatalf("update = %d\n%s", rec.Code, rec.Body)
	}
	p, _ = store.GetStatusPage(t.Context(), e.db.Reader, id)
	if p.Title != "Acme" || p.Theme != "carbon" || p.Accent != "" || len(p.Groups) != 1 || len(p.Monitors) != 1 || len(p.Hosts) != 0 || !p.ShowPoweredBy || p.IncidentDays != 30 {
		t.Errorf("after edit = %+v groups=%v monitors=%v hosts=%v", p.StatusPage, p.Groups, p.Monitors, p.Hosts)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'status_page.updated' AND object_id = ?`, id); n != 1 {
		t.Errorf("%d updated audit events", n)
	}
	if rec := e.postAs(t, "a1", "/status-pages/missing", g); rec.Code != http.StatusNotFound {
		t.Errorf("update of an unknown page = %d", rec.Code)
	}
	if rec := e.getAs(t, "a1", "GET", "/status-pages/missing/edit"); rec.Code != http.StatusNotFound {
		t.Errorf("edit of an unknown page = %d", rec.Code)
	}

	// Delete asks first, then deletes the page and only the page.
	rec = e.postAs(t, "a1", "/status-pages/"+id+"/delete", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Delete Acme?") || e.count(t, `SELECT COUNT(*) FROM status_pages`) != 1 {
		t.Fatalf("delete without confirmation = %d", rec.Code)
	}
	rec = e.postAs(t, "a1", "/status-pages/"+id+"/delete", url.Values{"confirm": {"1"}})
	if rec.Code != 303 || e.count(t, `SELECT COUNT(*) FROM status_pages`) != 0 {
		t.Fatalf("delete = %d", rec.Code)
	}
	if e.count(t, `SELECT COUNT(*) FROM monitors`) != 2 || e.count(t, `SELECT COUNT(*) FROM status_page_monitors`) != 0 {
		t.Error("deleting a page touched monitors or left mappings")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'status_page.deleted' AND object_id = ?`, id); n != 1 {
		t.Errorf("%d deleted audit events", n)
	}
	if rec := e.postAs(t, "a1", "/status-pages/"+id+"/delete", url.Values{"confirm": {"1"}}); rec.Code != http.StatusNotFound {
		t.Errorf("second delete = %d", rec.Code)
	}
}

func TestStatusPageValidation(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	api := e.addMonitor(t, "API", "https://api.example.com")
	e.postAs(t, "a1", "/status-pages", func() url.Values {
		f := pageForm("First", "first")
		f.Set("hosts", "taken.example.com")
		return f
	}())

	cases := []struct {
		name   string
		mutate func(url.Values)
		field  string // the field the error is on
		msg    string
	}{
		{"no title", func(f url.Values) { f.Set("title", " ") }, "title", "Enter a title"},
		{"bad slug", func(f url.Values) { f.Set("slug", "My Page!") }, "slug", "lowercase letters"},
		{"slug taken", func(f url.Values) { f.Set("slug", "First") }, "slug", "already used"},
		{"bad visibility", func(f url.Values) { f.Set("visibility", "secret") }, "visibility", "Choose who"},
		{"bad theme", func(f url.Values) { f.Set("theme", "neon") }, "theme", "Choose a theme"},
		{"accent not a colour", func(f url.Values) { f.Set("accent", "blue") }, "accent", "#rrggbb"},
		{"accent unreadable", func(f url.Values) { f.Set("accent", "#f6f3ec") }, "accent", "Too little contrast"},
		{"accent unreadable on the chosen theme", func(f url.Values) { f.Set("accent", "#7da2ff") }, "accent", "paper theme"},
		{"days", func(f url.Values) { f.Set("incident_days", "0") }, "incident_days", "1 to 365"},
		{"password page without a password", func(f url.Values) { f.Set("visibility", "password") }, "password", "Enter a password"},
		{"short password", func(f url.Values) { f.Set("visibility", "password"); f.Set("password", "short") }, "password", "at least 8"},
		{"duplicate group", func(f url.Values) { f.Set("groups", "Web\nweb") }, "groups", "twice"},
		{"host with a scheme", func(f url.Values) { f.Set("hosts", "https://status.example.com") }, "hosts", "not a host name"},
		{"host with a port", func(f url.Values) { f.Set("hosts", "status.example.com:8080") }, "hosts", "not a host name"},
		{"instance's own host", func(f url.Values) { f.Set("hosts", "LOCALHOST") }, "hosts", "own address"},
		{"host mapped elsewhere", func(f url.Values) { f.Set("hosts", "Taken.example.com") }, "hosts", "already mapped"},
		{"no public name", func(f url.Values) { show(f, api, " ", "", "") }, "m_name_" + api, "name the public sees"},
		{"unknown group", func(f url.Values) { show(f, api, "API", "Nope", "") }, "m_group_" + api, "groups listed above"},
		{"bad position", func(f url.Values) { show(f, api, "API", "", "-3") }, "m_order_" + api, "0 to 9999"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := pageForm("Second", "second")
			c.mutate(f)
			rec := e.postAs(t, "a1", "/status-pages", f)
			body := rec.Body.String()
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			if !strings.Contains(body, c.msg) || !strings.Contains(body, `href="#`+c.field+`"`) {
				t.Errorf("no %q error linked to #%s in:\n%s", c.msg, c.field, body)
			}
			if n := e.count(t, `SELECT COUNT(*) FROM status_pages WHERE slug = 'second'`); n != 0 {
				t.Error("an invalid page was stored")
			}
		})
	}

	// Every problem is listed at once, and what was typed is kept.
	f := pageForm("Typed title", "Bad Slug")
	f.Set("incident_days", "x")
	f.Set("hosts", "bad_host.example.com")
	show(f, api, "", "", "")
	body := e.postAs(t, "a1", "/status-pages", f).Body.String()
	if !strings.Contains(body, "Fix 4 problems to save") || !strings.Contains(body, `value="Typed title"`) || !strings.Contains(body, `value="Bad Slug"`) {
		t.Errorf("combined errors:\n%s", body)
	}
}

func TestStatusPagePassword(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	f := pageForm("Private", "private")
	f.Set("visibility", "password")
	f.Set("password", "correct horse battery")
	if rec := e.postAs(t, "a1", "/status-pages", f); rec.Code != 303 {
		t.Fatalf("create = %d\n%s", rec.Code, rec.Body)
	}
	id := e.pageID(t, "private")
	var hash string
	if err := e.db.Reader.QueryRow(`SELECT password_hash FROM status_pages WHERE id = ?`, id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := auth.VerifyPassword("correct horse battery", hash); err != nil || !ok || !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("stored hash %q does not verify as Argon2id: %v", hash, err)
	}

	// The password is write-only: never in the form, the audit log or logs.
	edit := e.getAs(t, "a1", "GET", "/status-pages/"+id+"/edit").Body.String()
	if strings.Contains(edit, "correct horse") || strings.Contains(edit, hash) || !strings.Contains(edit, "leave this empty to keep it") {
		t.Error("the edit form shows the password or does not say one is set")
	}
	var meta string
	_ = e.db.Reader.QueryRow(`SELECT COALESCE(metadata_json, '') FROM audit_events WHERE event_type = 'status_page.created'`).Scan(&meta)
	if strings.Contains(meta, "correct horse") || strings.Contains(meta, "argon2") || strings.Contains(e.logs.String(), "correct horse") {
		t.Error("the password reached the audit log or the logs")
	}

	// Saving with an empty password keeps it; a new one replaces it.
	g := pageForm("Private", "private")
	g.Set("visibility", "password")
	if rec := e.postAs(t, "a1", "/status-pages/"+id, g); rec.Code != 303 {
		t.Fatalf("save without a password = %d\n%s", rec.Code, rec.Body)
	}
	var kept string
	_ = e.db.Reader.QueryRow(`SELECT password_hash FROM status_pages WHERE id = ?`, id).Scan(&kept)
	if kept != hash {
		t.Error("an empty password field changed the stored password")
	}
	g.Set("password", "another long password")
	e.postAs(t, "a1", "/status-pages/"+id, g)
	_ = e.db.Reader.QueryRow(`SELECT password_hash FROM status_pages WHERE id = ?`, id).Scan(&kept)
	if ok, _, _ := auth.VerifyPassword("another long password", kept); !ok {
		t.Error("a new password was not stored")
	}

	// A password typed on a page that is not password-protected is not kept.
	h := pageForm("Private", "private")
	h.Set("password", "ignored password")
	e.postAs(t, "a1", "/status-pages/"+id, h)
	if n := e.count(t, `SELECT COUNT(*) FROM status_pages WHERE id = ? AND password_hash IS NOT NULL`, id); n != 0 {
		t.Error("a public page kept a password hash")
	}
}

func TestStatusPageUnlistedAddress(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", testPassword)
	e.addUser(t, "v1", "viewer", "viewer", testPassword)

	f := pageForm("Partners", "partners")
	f.Set("visibility", "unlisted")
	rec := e.postAs(t, "a1", "/status-pages", f)
	m := secretAddress.FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || m == nil {
		t.Fatalf("create = %d, no secret address shown:\n%s", rec.Code, rec.Body)
	}
	token := m[1]
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q: the address page must not be cached", cc)
	}
	id := e.pageID(t, "partners")
	sum := sha256.Sum256(decodeBase32(t, token))
	var stored []byte
	if err := e.db.Reader.QueryRow(`SELECT unlisted_token_hash FROM status_pages WHERE id = ?`, id).Scan(&stored); err != nil || string(stored) != string(sum[:]) {
		t.Fatalf("stored hash does not match the token's SHA-256: %v", err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM status_pages WHERE description = ? OR title = ? OR slug = ?`, token, token, token); n != 0 {
		t.Error("the token itself was stored")
	}

	// Shown once: not on the list, the edit page or after a plain save.
	edit := e.getAs(t, "a1", "GET", "/status-pages/"+id+"/edit").Body.String()
	if strings.Contains(edit, token) || !strings.Contains(edit, "Issue a new address") || !strings.Contains(edit, "cannot be shown again") {
		t.Error("the edit page shows the token or offers no new one")
	}
	if strings.Contains(e.getAs(t, "a1", "GET", "/status-pages").Body.String(), token) {
		t.Error("the list shows the token")
	}
	rec = e.postAs(t, "a1", "/status-pages/"+id, f)
	if rec.Code != 303 || strings.Contains(rec.Body.String(), token) {
		t.Errorf("a plain save = %d: it must keep the address and not show it", rec.Code)
	}
	var again []byte
	_ = e.db.Reader.QueryRow(`SELECT unlisted_token_hash FROM status_pages WHERE id = ?`, id).Scan(&again)
	if string(again) != string(stored) {
		t.Error("a plain save replaced the token")
	}

	// A viewer cannot regenerate; a stale session is sent to re-authenticate.
	path := "/status-pages/" + id + "/token"
	if rec := e.postAs(t, "v1", path, nil); rec.Code != http.StatusForbidden {
		t.Errorf("viewer = %d", rec.Code)
	}
	cookie, sess := e.signIn(t, "a1")
	old := time.Now().Add(-auth.ReauthWindow - time.Minute).UTC().Format(time.RFC3339)
	if _, err := e.db.Writer.Exec(`UPDATE sessions SET reauthenticated_at = ? WHERE id = ?`, old, sess.ID); err != nil {
		t.Fatal(err)
	}
	r := withCookie(req("POST", path, url.Values{CSRFFormField: {e.csrf.token(sess.ID)}}), cookie)
	r.Header.Set("Referer", "http://example.com/status-pages/"+id+"/edit")
	if rec := e.serve(r); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/reauth?next=") {
		t.Errorf("stale session = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	_ = e.db.Reader.QueryRow(`SELECT unlisted_token_hash FROM status_pages WHERE id = ?`, id).Scan(&again)
	if string(again) != string(stored) {
		t.Fatal("a refused regeneration changed the token")
	}

	// A recent session gets a new address; the old hash is gone.
	rec = e.postAs(t, "a1", path, nil)
	n := secretAddress.FindStringSubmatch(rec.Body.String())
	if rec.Code != 200 || n == nil || n[1] == token || !strings.Contains(rec.Body.String(), "old one has stopped working") {
		t.Fatalf("regenerate = %d", rec.Code)
	}
	_ = e.db.Reader.QueryRow(`SELECT unlisted_token_hash FROM status_pages WHERE id = ?`, id).Scan(&again)
	oldSum, newSum := sha256.Sum256(decodeBase32(t, token)), sha256.Sum256(decodeBase32(t, n[1]))
	if string(again) == string(oldSum[:]) || string(again) != string(newSum[:]) {
		t.Error("the stored hash is not the new token's")
	}
	if c := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'status_page.token_regenerated' AND object_id = ?`, id); c != 1 {
		t.Errorf("%d regeneration audit events", c)
	}
	if strings.Contains(e.logs.String(), token) || strings.Contains(e.logs.String(), n[1]) {
		t.Error("a token was logged")
	}

	// Only unlisted pages have one; leaving the mode drops it.
	pub := e.postAs(t, "a1", "/status-pages", pageForm("Open", "open"))
	_ = pub
	if rec := e.postAs(t, "a1", "/status-pages/"+e.pageID(t, "open")+"/token", nil); rec.Code != http.StatusNotFound {
		t.Errorf("token for a public page = %d", rec.Code)
	}
	if rec := e.postAs(t, "a1", "/status-pages/missing/token", nil); rec.Code != http.StatusNotFound {
		t.Errorf("token for an unknown page = %d", rec.Code)
	}
	g := pageForm("Partners", "partners")
	e.postAs(t, "a1", "/status-pages/"+id, g)
	if c := e.count(t, `SELECT COUNT(*) FROM status_pages WHERE id = ? AND unlisted_token_hash IS NOT NULL`, id); c != 0 {
		t.Error("a page that is no longer unlisted kept its token")
	}
	// Coming back issues a fresh address, shown once again.
	rec = e.postAs(t, "a1", "/status-pages/"+id, f)
	if back := secretAddress.FindStringSubmatch(rec.Body.String()); rec.Code != 200 || back == nil || back[1] == token || back[1] == n[1] {
		t.Errorf("unlisted again = %d, want a fresh address", rec.Code)
	}
}

func decodeBase32(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(s))
	if err != nil || len(raw) != 16 {
		t.Fatalf("token %q is not 128 bits of base32: %v", s, err)
	}
	return raw
}
