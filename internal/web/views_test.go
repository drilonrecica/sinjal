package web

import (
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/recent"
	"github.com/drilonrecica/sinjal/internal/store"
)

var columnNameRe = regexp.MustCompile(`<span class="column-name">([^<]*)</span>`)

// listNames returns the monitor names of GET /fragments/monitors?query as
// user, in order.
func (e *appEnv) listNames(t *testing.T, user, query string) []string {
	t.Helper()
	rec := e.getAs(t, user, "GET", "/fragments/monitors?"+query)
	if rec.Code != 200 {
		t.Fatalf("%s = %d", query, rec.Code)
	}
	var out []string
	for _, m := range columnNameRe.FindAllStringSubmatch(rec.Body.String(), -1) {
		out = append(out, m[1])
	}
	return out
}

// The column's search and filters (docs/29): name, tag, public label and,
// for admins, host; states including flapping; type; tag; TLS expiring;
// recently failing; slowest. They combine with AND.
func TestColumnFilters(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	web := e.addMonitor(t, "Web", "https://web.example.com", "prod", "eu")
	db := e.addMonitor(t, "Database", "https://db.internal.test", "prod")
	cert := e.addMonitor(t, "Cert", "https://cert.example.org")
	flap := e.addMonitor(t, "Flap", "https://flap.example.org", "Staging")

	soon := store.FormatTime(time.Now().Add(10 * 24 * time.Hour))
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET current_state = 'down' WHERE id = ?`, db); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET tls_not_after = ? WHERE id = ?`, soon, cert); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET current_state = 'up', flapping_since = ? WHERE id = ?`, store.FormatTime(time.Now()), flap); err != nil {
		t.Fatal(err)
	}
	e.engine.Recent().Push(web, recent.Check{At: time.Now().Unix(), MS: 40, OK: true})
	e.engine.Recent().Push(db, recent.Check{At: time.Now().Unix(), MS: 900, Kind: "timeout"})
	e.engine.Recent().Push(cert, recent.Check{At: time.Now().Unix(), MS: 300, OK: true})

	// A public label is searchable.
	e.mustExecWeb(t, `INSERT INTO status_pages (id, slug, title, visibility, theme, created_at, updated_at) VALUES ('p1', 'p', 'P', 'public', 'daylight', 'x', 'x')`)
	e.mustExecWeb(t, `INSERT INTO status_page_monitors (status_page_id, monitor_id, display_name, sort_order) VALUES ('p1', ?, 'Storefront', 0)`, web)

	for name, c := range map[string]struct {
		user, query string
		want        []string
	}{
		"all, by name":       {"a1", "", []string{"Cert", "Database", "Flap", "Web"}},
		"name":               {"a1", "q=data", []string{"Database"}},
		"case":               {"a1", "q=WEB", []string{"Web"}},
		"tag":                {"a1", "q=eu", []string{"Web"}},
		"public label":       {"a1", "q=storefront", []string{"Web"}},
		"host for admins":    {"a1", "q=db.internal", []string{"Database"}},
		"host not viewers":   {"v1", "q=db.internal", nil},
		"state down":         {"a1", "state=down", []string{"Database"}},
		"flapping":           {"a1", "state=flapping", []string{"Flap"}},
		"states any of":      {"a1", "state=down&state=flapping", []string{"Database", "Flap"}},
		"pending":            {"a1", "state=pending", []string{"Cert", "Web"}},
		"tag filter":         {"a1", "tag=prod", []string{"Database", "Web"}},
		"tag any case":       {"a1", "tag=staging", []string{"Flap"}},
		"tag and state":      {"a1", "tag=prod&state=down", []string{"Database"}},
		"type none":          {"a1", "type=tcp", nil},
		"type http":          {"a1", "type=http&q=cert", []string{"Cert"}},
		"tls expiring":       {"a1", "tls=1", []string{"Cert"}},
		"recently failing":   {"a1", "failing=1", []string{"Database"}},
		"slowest":            {"a1", "sort=slowest", []string{"Database", "Cert", "Web", "Flap"}},
		"invalid dropped":    {"a1", "state=sideways&type=ftp&sort=random&tls=2", []string{"Cert", "Database", "Flap", "Web"}},
		"viewers filter too": {"v1", "state=down", []string{"Database"}},
	} {
		if got := e.listNames(t, c.user, c.query); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s (%s): %v, want %v", name, c.query, got, c.want)
		}
	}
}

func (e *appEnv) mustExecWeb(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.db.Writer.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// The list state is the URL's query: the shell starts the search box,
// chip and column from the page's own address, the column's list keeps it
// when it refreshes, and the column's own form puts it in the address.
func TestColumnURLState(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addMonitor(t, "Web", "https://web.example.com")

	page := e.getAs(t, "a1", "GET", "/monitors?q=we%22b&state=down&tag=prod&junk=1").Body.String()
	for _, want := range []string{
		`value="we&#34;b"`, // the text, escaped
		`hx-get="/fragments/monitors?q=we%22b&amp;state=down&amp;tag=prod"`,
		`hx-get="/fragments/column-filters?q=we%22b&amp;state=down&amp;tag=prod"`,
		`<input type="hidden" name="state" value="down">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, "junk") {
		t.Error("an unknown parameter is carried into the column")
	}

	frag := e.getAs(t, "a1", "GET", "/fragments/monitors?state=down&tag=prod").Body.String()
	if !strings.Contains(frag, `hx-get="/fragments/monitors?state=down&amp;tag=prod"`) {
		t.Errorf("the refreshing list drops its state:\n%s", frag)
	}
	if !strings.Contains(frag, "No monitors match.") {
		t.Error("a filtered empty list does not say so")
	}

	// The column's form asks with its id: the answer replaces the address.
	cookie, _ := e.signIn(t, "a1")
	r := withCookie(req("GET", "/fragments/monitors?q=web&state=up", nil), cookie)
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Trigger", "column-filter")
	r.Header.Set("HX-Current-URL", "http://sinjal.test/monitors/abc?old=1")
	if got := e.serve(r).Header().Get("HX-Replace-Url"); got != "/monitors/abc?q=web&state=up" {
		t.Errorf("HX-Replace-Url = %q", got)
	}
	r = withCookie(req("GET", "/fragments/monitors", nil), cookie)
	r.Header.Set("HX-Trigger", "other")
	r.Header.Set("HX-Current-URL", "http://sinjal.test/monitors/abc")
	if got := e.serve(r).Header().Get("HX-Replace-Url"); got != "" {
		t.Errorf("another trigger set HX-Replace-Url = %q", got)
	}
}

// Saved views belong to the user who made them, viewers included; a view
// is shown as a link with its query.
func TestSavedViewsRoutes(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	e.addUser(t, "v2", "viewer2", "viewer", "")

	rec := e.postAs(t, "v1", "/views", url.Values{"view_name": {"Down prod"}, "state": {"down"}, "tag": {"prod"}, "sort": {"slowest"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "View saved.") ||
		!strings.Contains(rec.Body.String(), `href="/monitors?sort=slowest&amp;state=down&amp;tag=prod"`) {
		t.Fatalf("save = %d:\n%s", rec.Code, rec.Body.String())
	}
	views, err := store.ListSavedViews(t.Context(), e.db.Reader, "v1")
	if err != nil || len(views) != 1 || views[0].Sort != store.SortSlowest {
		t.Fatalf("stored = %+v, %v", views, err)
	}
	id := views[0].ID

	// The panel lists only the caller's views.
	if p := e.getAs(t, "v1", "GET", "/fragments/column-filters").Body.String(); !strings.Contains(p, "Down prod") {
		t.Error("the owner's panel lacks the view")
	}
	if p := e.getAs(t, "v2", "GET", "/fragments/column-filters").Body.String(); strings.Contains(p, "Down prod") || !strings.Contains(p, "None yet.") {
		t.Error("another user sees the view")
	}

	// A duplicate name and an empty one are explained in the panel.
	for _, name := range []string{"down PROD", " "} {
		rec = e.postAs(t, "v1", "/views", url.Values{"view_name": {name}})
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `role="alert"`) {
			t.Errorf("name %q: %d:\n%s", name, rec.Code, rec.Body.String())
		}
	}

	// Another user cannot update or delete it.
	for _, path := range []string{"/views/" + id, "/views/" + id + "/delete"} {
		rec = e.postAs(t, "v2", path, url.Values{"state": {"up"}})
		if !strings.Contains(rec.Body.String(), "no longer exists") {
			t.Errorf("%s as another user:\n%s", path, rec.Body.String())
		}
	}
	if views, _ := store.ListSavedViews(t.Context(), e.db.Reader, "v1"); len(views) != 1 || !reflect.DeepEqual(views[0].Filters.States, []string{"down"}) {
		t.Errorf("the view was changed: %+v", views)
	}

	rec = e.postAs(t, "v1", "/views/"+id, url.Values{"state": {"up"}})
	if !strings.Contains(rec.Body.String(), "View updated.") {
		t.Fatalf("update:\n%s", rec.Body.String())
	}
	if views, _ := store.ListSavedViews(t.Context(), e.db.Reader, "v1"); len(views) != 1 || views[0].Name != "Down prod" ||
		!reflect.DeepEqual(views[0].Filters.States, []string{"up"}) {
		t.Errorf("after update: %+v", views)
	}

	rec = e.postAs(t, "v1", "/views/"+id+"/delete", nil)
	if !strings.Contains(rec.Body.String(), "View deleted.") {
		t.Fatalf("delete:\n%s", rec.Body.String())
	}
	if views, _ := store.ListSavedViews(t.Context(), e.db.Reader, "v1"); len(views) != 0 {
		t.Errorf("after delete: %+v", views)
	}

	// No token, no change.
	cookie, _ := e.signIn(t, "v1")
	if rec := e.serve(withCookie(req("POST", "/views", url.Values{"view_name": {"x"}}), cookie)); rec.Code != 403 {
		t.Errorf("without CSRF = %d", rec.Code)
	}
}
