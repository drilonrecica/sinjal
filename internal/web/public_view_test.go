package web

import (
	"bytes"
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/maintenance"
	"github.com/drilonrecica/sinjal/internal/statuspage"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/web/templates"
)

// The view model the four layouts share (docs/12 "What every layout
// shares"): Since, Downtime, LastIssue and Calendar, for a new monitor, a
// paused one, one in maintenance, one down now and one with an incident.
func TestPublicViewFields(t *testing.T) {
	e := newAppEnv(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	add := func(name string, created time.Time, enabled bool, state string) string {
		id, err := store.CreateMonitor(ctx, e.db, store.MonitorInput{Name: name, Enabled: enabled,
			HTTP: store.HTTPConfig{URL: "https://" + name + ".example.com/"}}, created)
		if err != nil {
			t.Fatal(err)
		}
		e.exec(t, `UPDATE monitors SET current_state = ? WHERE id = ?`, state, id)
		return id
	}
	long := now.AddDate(0, 0, -120)
	old := add("old", long, true, "up")
	fresh := add("fresh", time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC), true, "up")
	paused := add("paused", long, false, "paused")
	maint := add("maint", long, true, "up")
	down := add("down", long, true, "down")

	issue := time.Date(2026, 10, 23, 9, 14, 0, 0, time.UTC)
	issueEnd := issue.Add(26 * time.Minute)
	e.addIncident(t, "past", old, issue, &issueEnd, false)
	e.addIncident(t, "now", down, now.Add(-18*time.Minute), nil, false)
	if _, err := store.CreateMaintenance(ctx, e.db, maintenance.Window{Name: "w", Start: now.Add(-time.Hour), Duration: 2 * time.Hour,
		Recurrence: maintenance.None, ExcludeUptime: true, Scope: maintenance.Scope{Monitors: []string{maint}}}, long); err != nil {
		t.Fatal(err)
	}
	pageID := e.addPage(t, store.StatusPageInput{Slug: "v", Title: "V", Layout: "logbook", Monitors: []store.StatusPageMonitorInput{
		{MonitorID: old, DisplayName: "Old", Sort: 1}, {MonitorID: fresh, DisplayName: "Fresh", Sort: 2},
		{MonitorID: paused, DisplayName: "Paused", Sort: 3}, {MonitorID: maint, DisplayName: "Maint", Sort: 4},
		{MonitorID: down, DisplayName: "Down", Sort: 5}}})
	p, err := store.GetStatusPage(ctx, e.db.Reader, pageID)
	if err != nil {
		t.Fatal(err)
	}
	h := NewPublic(e.db, "", nil, time.UTC, quietLoggerOnly())
	v, err := h.build(ctx, p, now)
	if err != nil {
		t.Fatal(err)
	}

	if v.Layout != "logbook" || v.Zone != "UTC" {
		t.Errorf("layout %q, zone %q", v.Layout, v.Zone)
	}
	rows := map[string]templates.PublicRow{}
	for _, r := range v.Groups[0].Rows {
		rows[r.Name] = r
	}
	for _, c := range []struct {
		name, state, since string
		sinceDay           int
		downtime           string
	}{
		{"Old", templates.StateUp, "", 0, "Down 26 min in 90 days"},
		{"Fresh", templates.StateUp, "Monitoring since 25 Oct", 88, "No downtime since 25 Oct"},
		{"Paused", templates.StatePaused, "", 0, ""},
		{"Maint", templates.StateMaintenance, "", 0, "No downtime in 90 days"},
		{"Down", templates.StateDown, "", 0, "Down now, for 18 min"},
	} {
		r := rows[c.name]
		if r.State != c.state || r.Since != c.since || r.SinceDay != c.sinceDay || r.Downtime != c.downtime {
			t.Errorf("%s: state %q, since %q at %d, downtime %q; want %q, %q at %d, %q",
				c.name, r.State, r.Since, r.SinceDay, r.Downtime, c.state, c.since, c.sinceDay, c.downtime)
		}
	}
	if s := rows["Fresh"].StripText; !strings.HasPrefix(s, "Last 90 days: ") || !strings.HasSuffix(s, ". Monitoring since 25 Oct") {
		t.Errorf("a new monitor's strip text = %q", s)
	}

	if want := "The last interruption was 3 days ago, 26 minutes on Old."; v.LastIssue != want {
		t.Errorf("LastIssue = %q, want %q", v.LastIssue, want)
	}
	// The headline names the service that is down; the time so far is the
	// oldest ongoing incident's; the counts are every service and those up.
	if v.Names != "Down" || v.Verb != "is down" || v.DownFor != "18 minutes" || v.Services != 5 || v.Operational != 2 {
		t.Errorf("headline %q %q, down for %q, %d services, %d operational", v.Names, v.Verb, v.DownFor, v.Services, v.Operational)
	}
	// Each incident carries its day, clock time and duration in words.
	if len(v.Incidents) != 2 {
		t.Fatalf("%d incidents", len(v.Incidents))
	}
	for i, want := range []templates.PublicIncident{
		{Service: "Down", Active: true, Date: "Today", Time: "11:42", Long: "18 minutes", Duration: "18m"},
		{Service: "Old", Date: "Fri 23 Oct", Time: "09:14", Long: "26 minutes", Duration: "26m"},
	} {
		got := v.Incidents[i]
		if got.Service != want.Service || got.Active != want.Active || got.Date != want.Date || got.Time != want.Time || got.Long != want.Long || got.Duration != want.Duration {
			t.Errorf("incident %d = %+v, want %+v", i, got, want)
		}
	}

	// The calendar: the worst class over the services, and the listed
	// incidents (the active one first) on the days they touched.
	if len(v.Calendar) != 90 {
		t.Fatalf("%d calendar days", len(v.Calendar))
	}
	today, past, first := v.Calendar[89], v.Calendar[86], v.Calendar[0]
	if today.Date != "2026-10-26" || today.Label != "Mon 26 Oct" || today.Class != templates.DayPartial || !slices.Equal(today.Incidents, []int{0}) {
		t.Errorf("today = %+v", today)
	}
	if past.Date != "2026-10-23" || past.Class != templates.DayPartial || !slices.Equal(past.Incidents, []int{1}) {
		t.Errorf("23 Oct = %+v", past)
	}
	if first.Class != templates.DayUp || len(first.Incidents) != 0 {
		t.Errorf("the first day = %+v", first)
	}

	// A page without incidents.
	quietID := e.addPage(t, store.StatusPageInput{Slug: "q", Title: "Q", Monitors: []store.StatusPageMonitorInput{{MonitorID: fresh, DisplayName: "Fresh"}}})
	q, err := store.GetStatusPage(ctx, e.db.Reader, quietID)
	if err != nil {
		t.Fatal(err)
	}
	if v, err = h.build(ctx, q, now); err != nil {
		t.Fatal(err)
	}
	if v.LastIssue != "No interruptions in the last 30 days." || v.Layout != statuspage.DefaultLayout || v.DownFor != "" || v.Names != "" || v.Services != 1 || v.Operational != 1 {
		t.Errorf("quiet page: LastIssue %q, layout %q, down for %q, names %q, %d services, %d operational", v.LastIssue, v.Layout, v.DownFor, v.Names, v.Services, v.Operational)
	}
	for i, d := range v.Calendar {
		want := templates.DayNoData
		if i >= 88 {
			want = templates.DayUp
		}
		if d.Class != want {
			t.Errorf("quiet day %d = %s, want %s", i, d.Class, want)
		}
	}
}

func TestDowntimeText(t *testing.T) {
	r := func(down int64) incident.Ratio { return incident.Ratio{Up: 90*86400 - down, Den: 90 * 86400} }
	for _, c := range []struct {
		ratio   incident.Ratio
		since   string
		downFor time.Duration
		want    string
	}{
		{r(0), "", 0, "No downtime in 90 days"},
		{r(0), "25 Oct", 0, "No downtime since 25 Oct"},
		{r(26 * 60), "", 0, "Down 26 min in 90 days"},
		{r(65*60 + 30), "", 0, "Down 1 h 5 min in 90 days"},
		{r(12 * 60), "25 Oct", 0, "Down 12 min since 25 Oct"},
		{r(40), "", 0, "Down under 1 min in 90 days"},
		{r(26 * 60), "", 18 * time.Minute, "Down now, for 18 min"},
		{incident.Ratio{}, "", 0, ""},
	} {
		if got := downtimeText(c.ratio, c.since, c.downFor); got != c.want {
			t.Errorf("downtimeText(%v, %q, %v) = %q, want %q", c.ratio, c.since, c.downFor, got, c.want)
		}
	}
}

func TestHumanDurations(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second: "under 1 min", 26 * time.Minute: "26 min", time.Hour: "1 h", 65 * time.Minute: "1 h 5 min",
		24 * time.Hour: "1 d", 51 * time.Hour: "2 d 3 h",
	} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{
		30 * time.Second: "less than a minute", time.Minute: "1 minute", 12 * time.Minute: "12 minutes", 2 * time.Hour: "2 hours",
		65 * time.Minute: "1 hour 5 minutes", 51 * time.Hour: "2 days 3 hours",
	} {
		if got := longDuration(d); got != want {
			t.Errorf("longDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestLastIssue(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	now := time.Date(2026, 10, 26, 0, 30, 0, 0, loc)
	ended := func(start time.Time, d time.Duration) store.PageIncident {
		end := start.Add(d)
		return store.PageIncident{DisplayName: "Web", StartedAt: start, EndedAt: &end}
	}
	// Late yesterday local time is yesterday, though under a day ago.
	if got := lastIssue([]store.PageIncident{ended(now.Add(-time.Hour), time.Minute)}, 30, now, loc); got != "The last interruption was yesterday, 1 minute on Web." {
		t.Errorf("yesterday: %q", got)
	}
	active := store.PageIncident{DisplayName: "Shop", StartedAt: now.Add(-5 * time.Minute)}
	if got := lastIssue([]store.PageIncident{active, ended(now.Add(-10*time.Minute), 2*time.Minute)}, 30, now, loc); got != "The last interruption was today, 2 minutes on Web." {
		t.Errorf("today, behind an active one: %q", got)
	}
	if got := lastIssue([]store.PageIncident{active}, 1, now, loc); got != "No interruptions in the last day." {
		t.Errorf("only an active one: %q", got)
	}
}

// An incident across midnight touches both days; the worst class wins.
func TestCalendarOf(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	days := stripBounds(now, time.UTC)
	strip := func(classes map[int]string) []templates.Day {
		s := make([]templates.Day, 90)
		for i := range s {
			s[i].Class = templates.DayUp
			if c, ok := classes[i]; ok {
				s[i].Class = c
			}
		}
		return s
	}
	a := strip(map[int]string{10: templates.DayMaintenance, 11: templates.DayPartial, 12: templates.DayNoData})
	b := strip(map[int]string{10: templates.DayPartial, 11: templates.DayDown, 12: templates.DayNoData})
	start := days[87].Add(-time.Hour)
	end := start.Add(2 * time.Hour)
	cal := calendarOf([][]templates.Day{a, b}, days, []store.PageIncident{{StartedAt: start, EndedAt: &end}}, now)
	for i, want := range map[int]string{10: templates.DayPartial, 11: templates.DayDown, 12: templates.DayNoData, 13: templates.DayUp} {
		if cal[i].Class != want {
			t.Errorf("day %d = %s, want %s", i, cal[i].Class, want)
		}
	}
	for i, d := range cal {
		if touched := i == 86 || i == 87; touched != slices.Equal(d.Incidents, []int{0}) {
			t.Errorf("day %d incidents = %v", i, d.Incidents)
		}
	}
	if empty := calendarOf(nil, days, nil, now); empty[0].Class != templates.DayNoData {
		t.Errorf("a page without services = %s", empty[0].Class)
	}
}

// Subscribe links to the feed of the address the page is served at; the
// cached view is shared by all of them.
func TestPublicFeedURL(t *testing.T) {
	e := newAppEnv(t)
	token, hash, err := statuspage.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	e.addPage(t, store.StatusPageInput{Slug: "hidden", Title: "Hidden", Visibility: "unlisted", TokenHash: hash})
	e.mappedPage(t, store.StatusPageInput{Slug: "shop", Title: "Shop status"})
	for _, c := range []struct {
		path, host, want string
	}{
		{"/status/shop", "", `href="/status/shop/feed.xml"`},
		{"/", mappedHost, `href="/feed.xml"`},
		{"/s/" + token, "", `href="/s/` + token + `/feed.xml"`},
	} {
		r := req("GET", c.path, nil)
		if c.host != "" {
			r = onHost(r, c.host)
		}
		body := e.serve(r).Body.String()
		if !strings.Contains(body, `<a class="public-subscribe" `+c.want+`>`) {
			t.Errorf("%s on %q: no Subscribe link %s", c.path, c.host, c.want)
		}
	}
}

// api.json and feed.xml do not depend on the layout (docs/12 "What every
// layout shares"): the same page in every layout gives the same bytes.
func TestPublicMachineFormsIgnoreLayout(t *testing.T) {
	e := newAppEnv(t)
	e.feedFixture(t)
	h := NewPublic(e.db, "", []byte("0123456789abcdef0123456789abcdef"), time.UTC, quietLoggerOnly())
	now := time.Now()
	h.now = func() time.Time { return now }
	var first [2][]byte
	for i, layout := range statuspage.Layouts {
		e.exec(t, `UPDATE status_pages SET layout = ?, updated_at = ? WHERE slug = 'main'`, layout, store.FormatTime(now.Add(time.Duration(i)*time.Second)))
		p, err := store.GetStatusPageBySlug(context.Background(), e.db.Reader, "main")
		if err != nil {
			t.Fatal(err)
		}
		for k, kind := range []int{kindJSON, kindFeed} {
			rec := httptest.NewRecorder()
			h.serve(rec, req("GET", "/status/main/x", nil), p, pageAccess{base: "/status/main", kind: kind})
			if rec.Code != 200 {
				t.Fatalf("%q kind %d = %d", layout, kind, rec.Code)
			}
			if i == 0 {
				first[k] = rec.Body.Bytes()
			} else if !bytes.Equal(rec.Body.Bytes(), first[k]) {
				t.Errorf("kind %d differs in layout %q:\n%s\nwant\n%s", kind, layout, rec.Body.Bytes(), first[k])
			}
		}
	}
	if strings.Contains(string(first[0]), "layout") || strings.Contains(string(first[0]), "calendar") {
		t.Errorf("api.json carries layout fields: %s", first[0])
	}
}
