package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func pageInput(slug string) StatusPageInput {
	return StatusPageInput{Slug: slug, Title: "Status " + slug, Visibility: "public", Theme: "paper", IncidentDays: 30, ShowPoweredBy: true}
}

func TestStatusPageCreateAndRead(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	api, site := create(t, d, sample("api")), create(t, d, sample("site"))

	in := pageInput("main")
	in.Description = "All our things"
	in.Accent = "#3e67a8"
	in.Groups = []string{"Web", "Infrastructure"}
	in.Monitors = []StatusPageMonitorInput{
		{MonitorID: site, Group: "Web", DisplayName: "Website", Sort: 1},
		{MonitorID: api, Group: "web", DisplayName: "API", ShowLatency: true, Sort: 0}, // group names match case-insensitively
	}
	in.Hosts = []string{"status.example.com", "a.example.com"}
	id, err := CreateStatusPage(ctx, d, in, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := GetStatusPage(ctx, d.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != "main" || got.Title != "Status main" || got.Description != "All our things" || got.Accent != "#3e67a8" ||
		got.Visibility != "public" || got.Theme != "paper" || got.IncidentDays != 30 || !got.ShowPoweredBy ||
		got.HasToken || got.HasPassword || got.LogoPath != "" || !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
		t.Errorf("page = %+v", got.StatusPage)
	}
	if len(got.Groups) != 2 || got.Groups[0].Name != "Web" || got.Groups[1].Name != "Infrastructure" {
		t.Errorf("groups = %+v, want Web then Infrastructure", got.Groups)
	}
	if len(got.Monitors) != 2 || got.Monitors[0].MonitorID != api || got.Monitors[0].DisplayName != "API" || !got.Monitors[0].ShowLatency ||
		got.Monitors[0].GroupID != got.Groups[0].ID || got.Monitors[1].MonitorID != site || got.Monitors[1].ShowLatency {
		t.Errorf("monitors = %+v, want API then Website in the Web group", got.Monitors)
	}
	if !reflect.DeepEqual(got.Hosts, []string{"a.example.com", "status.example.com"}) {
		t.Errorf("hosts = %v", got.Hosts)
	}

	if _, err := GetStatusPage(ctx, d.Reader, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown page = %v, want ErrNotFound", err)
	}
	list, err := ListStatusPages(ctx, d.Reader)
	if err != nil || len(list) != 1 || list[0].Monitors != 2 || !reflect.DeepEqual(list[0].Hosts, got.Hosts) {
		t.Errorf("list = %+v, %v", list, err)
	}
}

func TestStatusPageUniqueSlugAndHosts(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	a := pageInput("a")
	a.Hosts = []string{"status.example.com"}
	idA, err := CreateStatusPage(ctx, d, a, now)
	if err != nil {
		t.Fatal(err)
	}
	b := pageInput("a")
	b.Hosts = []string{"status.example.com"}
	_, err = CreateStatusPage(ctx, d, b, now)
	var fe FieldErrors
	if !errors.As(err, &fe) || fe["slug"] == "" || fe["hosts"] == "" {
		t.Fatalf("duplicate slug and host = %v, want both reported", err)
	}
	// A page may keep its own slug and hosts when saved again.
	a.Title = "Renamed"
	if err := UpdateStatusPage(ctx, d, idA, a, now); err != nil {
		t.Fatal(err)
	}
	// And may not take another page's.
	idB, err := CreateStatusPage(ctx, d, pageInput("b"), now)
	if err != nil {
		t.Fatal(err)
	}
	c := pageInput("a")
	if err := UpdateStatusPage(ctx, d, idB, c, now); !errors.As(err, &fe) || fe["slug"] == "" {
		t.Errorf("renaming onto another slug = %v", err)
	}
	if err := UpdateStatusPage(ctx, d, "nope", pageInput("z"), now); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of an unknown page = %v, want ErrNotFound", err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM status_pages`); n != 2 {
		t.Errorf("pages = %d, want 2: a failed save must leave nothing behind", n)
	}
}

func TestStatusPageSecretsFollowVisibility(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	// A password page needs a password.
	in := pageInput("p")
	in.Visibility = "password"
	var fe FieldErrors
	if _, err := CreateStatusPage(ctx, d, in, now); !errors.As(err, &fe) || fe["password"] == "" {
		t.Fatalf("password page without a password = %v", err)
	}
	in.PasswordHash = "$argon2id$stored"
	id, err := CreateStatusPage(ctx, d, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := GetStatusPage(ctx, d.Reader, id); !got.HasPassword || got.HasToken {
		t.Errorf("password page: %+v", got.StatusPage)
	}
	// Saving again without a new password keeps the stored one.
	in.PasswordHash = ""
	in.Title = "Edited"
	if err := UpdateStatusPage(ctx, d, id, in, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := GetStatusPage(ctx, d.Reader, id); !got.HasPassword || got.Title != "Edited" {
		t.Errorf("kept password: %+v", got.StatusPage)
	}
	// Leaving the mode drops the hash for good, so coming back asks again.
	in.Visibility = "public"
	if err := UpdateStatusPage(ctx, d, id, in, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := GetStatusPage(ctx, d.Reader, id); got.HasPassword {
		t.Error("a public page kept a password hash")
	}
	in.Visibility = "password"
	if err := UpdateStatusPage(ctx, d, id, in, now); !errors.As(err, &fe) || fe["password"] == "" {
		t.Errorf("back to password without one = %v", err)
	}

	// An unlisted page has a token hash; it is kept on save, dropped on
	// leaving, and a store call without one is a programming error.
	u := pageInput("u")
	u.Visibility = "unlisted"
	if _, err := CreateStatusPage(ctx, d, u, now); err == nil || errors.As(err, &fe) {
		t.Fatalf("unlisted without a token hash = %v, want a plain error", err)
	}
	u.TokenHash = []byte("hash-one")
	uid, err := CreateStatusPage(ctx, d, u, now)
	if err != nil {
		t.Fatal(err)
	}
	u.TokenHash = nil
	if err := UpdateStatusPage(ctx, d, uid, u, now); err != nil {
		t.Fatalf("save keeping the token: %v", err)
	}
	if got, _ := GetStatusPage(ctx, d.Reader, uid); !got.HasToken {
		t.Error("the token was lost on save")
	}
	if err := SetStatusPageToken(ctx, d, uid, []byte("hash-two"), now); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := d.Reader.QueryRow(`SELECT unlisted_token_hash FROM status_pages WHERE id = ?`, uid).Scan(&stored); err != nil || string(stored) != "hash-two" {
		t.Errorf("token hash = %q, %v; want the new one", stored, err)
	}
	if err := SetStatusPageToken(ctx, d, id, []byte("x"), now); !errors.Is(err, ErrNotFound) {
		t.Errorf("token for a page that is not unlisted = %v, want ErrNotFound", err)
	}
	u.Visibility = "public"
	if err := UpdateStatusPage(ctx, d, uid, u, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := GetStatusPage(ctx, d.Reader, uid); got.HasToken {
		t.Error("a public page kept an unlisted token")
	}
}

func TestStatusPageGroupsKeepIDsAndMonitorsFollowEdits(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	api, site := create(t, d, sample("api")), create(t, d, sample("site"))
	in := pageInput("main")
	in.Groups = []string{"Web", "Data"}
	in.Monitors = []StatusPageMonitorInput{{MonitorID: api, Group: "Web", DisplayName: "API"}, {MonitorID: site, Group: "Data", DisplayName: "Site", Sort: 1}}
	id, err := CreateStatusPage(ctx, d, in, now)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := GetStatusPage(ctx, d.Reader, id)

	// Reorder, rename the case of one, drop one, add one; unmap a monitor.
	in.Groups = []string{"data", "New", "Web"}
	in.Monitors = []StatusPageMonitorInput{{MonitorID: site, Group: "New", DisplayName: "Site"}}
	if err := UpdateStatusPage(ctx, d, id, in, now); err != nil {
		t.Fatal(err)
	}
	after, _ := GetStatusPage(ctx, d.Reader, id)
	names := []string{}
	for _, g := range after.Groups {
		names = append(names, g.Name)
	}
	if !reflect.DeepEqual(names, []string{"data", "New", "Web"}) {
		t.Fatalf("groups = %v, want the posted order", names)
	}
	if after.Groups[0].ID != before.Groups[1].ID || after.Groups[2].ID != before.Groups[0].ID {
		t.Error("groups that kept their name did not keep their id")
	}
	if len(after.Monitors) != 1 || after.Monitors[0].MonitorID != site || after.Monitors[0].GroupID != after.Groups[1].ID {
		t.Errorf("monitors = %+v, want only the site in New", after.Monitors)
	}

	in.Groups = nil
	in.Monitors = []StatusPageMonitorInput{{MonitorID: site, DisplayName: "Site"}}
	if err := UpdateStatusPage(ctx, d, id, in, now); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM status_page_groups`); n != 0 {
		t.Errorf("groups left = %d, want 0", n)
	}
	// A monitor that is gone, or a group that was never posted, is refused.
	in.Monitors = []StatusPageMonitorInput{{MonitorID: "nope", DisplayName: "x"}}
	var fe FieldErrors
	if err := UpdateStatusPage(ctx, d, id, in, now); !errors.As(err, &fe) || fe["monitors"] == "" {
		t.Errorf("unknown monitor = %v", err)
	}
	in.Monitors = []StatusPageMonitorInput{{MonitorID: site, Group: "Ghost", DisplayName: "x"}}
	if err := UpdateStatusPage(ctx, d, id, in, now); err == nil {
		t.Error("a monitor in an unposted group was accepted")
	}
	// Both failures rolled back: the page still shows the site.
	if got, _ := GetStatusPage(ctx, d.Reader, id); len(got.Monitors) != 1 || got.Monitors[0].DisplayName != "Site" {
		t.Errorf("after failed saves = %+v", got.Monitors)
	}
}

func TestStatusPageDeleteCascades(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	api := create(t, d, sample("api"))
	in := pageInput("main")
	in.Groups = []string{"Web"}
	in.Monitors = []StatusPageMonitorInput{{MonitorID: api, Group: "Web", DisplayName: "API"}}
	in.Hosts = []string{"status.example.com"}
	id, err := CreateStatusPage(ctx, d, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteStatusPage(ctx, d, id); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"status_pages", "status_page_groups", "status_page_monitors", "status_page_hosts"} {
		if n := count(t, d, `SELECT COUNT(*) FROM `+table); n != 0 {
			t.Errorf("%s rows after deleting the page = %d", table, n)
		}
	}
	if n := count(t, d, `SELECT COUNT(*) FROM monitors`); n != 1 {
		t.Error("deleting a page removed a monitor")
	}
	if err := DeleteStatusPage(ctx, d, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
}

func TestStatusPageLogoPath(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	id, err := CreateStatusPage(ctx, d, pageInput("main"), now)
	if err != nil {
		t.Fatal(err)
	}
	if prev, err := SetStatusPageLogo(ctx, d, id, "a.png", now); err != nil || prev != "" {
		t.Fatalf("first logo: %q, %v", prev, err)
	}
	if prev, err := SetStatusPageLogo(ctx, d, id, "b.jpg", now); err != nil || prev != "a.png" {
		t.Fatalf("replace: %q, %v; want the previous name back", prev, err)
	}
	// Saving the page's settings leaves the logo alone.
	in := pageInput("main")
	in.Title = "Renamed"
	if err := UpdateStatusPage(ctx, d, id, in, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := GetStatusPage(ctx, d.Reader, id); got.LogoPath != "b.jpg" || got.Title != "Renamed" {
		t.Errorf("after a settings save: %+v", got.StatusPage)
	}
	if prev, err := SetStatusPageLogo(ctx, d, id, "", now); err != nil || prev != "b.jpg" {
		t.Fatalf("remove: %q, %v", prev, err)
	}
	if got, _ := GetStatusPage(ctx, d.Reader, id); got.LogoPath != "" {
		t.Errorf("logo after removal = %q", got.LogoPath)
	}
	if _, err := SetStatusPageLogo(ctx, d, "nope", "c.png", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown page = %v", err)
	}
}

func TestStatusPageLookups(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	api := create(t, d, sample("api"))
	in := pageInput("main")
	in.Monitors = []StatusPageMonitorInput{{MonitorID: api, DisplayName: "API"}}
	in.Hosts = []string{"status.example.com"}
	id, err := CreateStatusPage(ctx, d, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := GetStatusPageBySlug(ctx, d.Reader, "main"); err != nil || p.ID != id || len(p.Monitors) != 1 || len(p.Hosts) != 1 {
		t.Errorf("by slug = %+v, %v", p, err)
	}
	if _, err := GetStatusPageBySlug(ctx, d.Reader, "other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown slug = %v", err)
	}
	if got, err := StatusPageIDByHost(ctx, d.Reader, "status.example.com"); err != nil || got != id {
		t.Errorf("by host = %q, %v", got, err)
	}
	if _, err := StatusPageIDByHost(ctx, d.Reader, "other.example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown host = %v", err)
	}

	hash := []byte("0123456789abcdef0123456789abcdef")
	u := pageInput("secret")
	u.Visibility, u.TokenHash = "unlisted", hash
	uid, err := CreateStatusPage(ctx, d, u, now)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := GetStatusPageByTokenHash(ctx, d.Reader, hash); err != nil || p.ID != uid {
		t.Errorf("by token = %+v, %v", p, err)
	}
	if _, err := GetStatusPageByTokenHash(ctx, d.Reader, []byte("wrong")); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown token = %v", err)
	}
}

func TestListPageIncidents(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	api, db, other := create(t, d, sample("api")), create(t, d, sample("db")), create(t, d, sample("other"))
	in := pageInput("main")
	in.Monitors = []StatusPageMonitorInput{{MonitorID: api, DisplayName: "Public API"}, {MonitorID: db, DisplayName: "Database"}}
	page, err := CreateStatusPage(ctx, d, in, now)
	if err != nil {
		t.Fatal(err)
	}
	since := now.Add(-30 * 24 * time.Hour)
	old := seedIncident(t, d.Writer, api, since.Add(-2*time.Hour), since.Add(-time.Hour), false) // ended before the window
	spans := seedIncident(t, d.Writer, api, since.Add(-time.Hour), since.Add(time.Hour), false)  // started before, ended inside
	recent := seedIncident(t, d.Writer, db, now.Add(-2*time.Hour), now.Add(-time.Hour), false)
	active := seedIncident(t, d.Writer, api, now.Add(-time.Minute), time.Time{}, false)
	seedIncident(t, d.Writer, other, now.Add(-time.Hour), time.Time{}, false) // not on the page
	for _, n := range []struct {
		id, msg   string
		published bool
	}{{recent, "private note", false}, {recent, "we restarted the database", true}, {old, "old", true}} {
		if _, err := AddIncidentNote(ctx, d, n.id, n.msg, n.published, now); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ListPageIncidents(ctx, d.Reader, page, since, 50)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, in := range got {
		ids = append(ids, in.ID)
	}
	if want := []string{active, recent, spans}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("incidents = %v, want %v", ids, want)
	}
	if got[0].DisplayName != "Public API" || got[0].EndedAt != nil || got[1].DisplayName != "Database" {
		t.Errorf("rows = %+v", got)
	}
	if len(got[1].Notes) != 1 || got[1].Notes[0].Message != "we restarted the database" || len(got[0].Notes) != 0 {
		t.Errorf("notes = %+v / %+v, want only the published one", got[1].Notes, got[0].Notes)
	}
	if got, _ := ListPageIncidents(ctx, d.Reader, page, since, 1); len(got) != 1 || got[0].ID != active {
		t.Errorf("limit 1 = %+v", got)
	}
	if got, err := ListPageIncidents(ctx, d.Reader, "nope", since, 50); err != nil || got != nil {
		t.Errorf("unknown page = %+v, %v", got, err)
	}
}
