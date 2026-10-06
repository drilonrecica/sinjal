package web

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/maintenance"
	"github.com/drilonrecica/sinjal/internal/store"
)

var ct0 = time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)

func cm(n int) time.Time { return ct0.Add(time.Duration(n) * time.Minute) }

// Overlapping states resolve paused, then down, then maintenance; time
// before the monitor existed is no data and time after now is left out.
func TestTimeline(t *testing.T) {
	iv := store.Intervals{
		Created:     cm(100),
		Paused:      []incident.Span{{From: cm(700), To: cm(800)}},
		Down:        []incident.Span{{From: cm(300), To: cm(400)}, {From: cm(750), To: cm(850)}, {From: cm(950)}},
		Maintenance: []incident.Span{{From: cm(350), To: cm(500)}},
	}
	segs := timeline(cm(0), cm(1000), cm(990), iv, time.UTC)
	var got []string
	total := 0.0
	for _, s := range segs {
		got = append(got, s.Class)
		total += s.W
	}
	want := []string{"none", "up", "down", "maintenance", "up", "paused", "down", "up", "down"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("segments %v, want %v", got, want)
	}
	if total < 999.9 || total > 1000.1 || segs[0].X != 0 || segs[0].W != 100/990.0*1000 {
		t.Fatalf("widths add up to %v; first %+v", total, segs[0])
	}
	if segs[2].Title != "Down: 6 Oct 05:00 to 6 Oct 06:40 (1h 40m)" {
		t.Fatalf("title %q", segs[2].Title)
	}
	if segs := timeline(cm(0), cm(10), cm(0), iv, time.UTC); segs != nil {
		t.Fatalf("a range after now: %+v", segs)
	}
}

func TestChartJSON(t *testing.T) {
	ms := time.Millisecond
	points := []store.LatencyPoint{
		{At: cm(0), Checks: 2, Samples: 2, Avg: 12340 * time.Microsecond, Max: 20 * ms},
		{At: cm(1), Checks: 1, Failures: 1},
	}
	iv := store.Intervals{
		Down:        []incident.Span{{From: cm(-5), To: cm(2)}, {From: cm(8)}},
		Maintenance: []incident.Span{{From: cm(3), To: cm(4)}},
	}
	series, overlays := chartJSON(points, cm(0), cm(10), cm(9), iv)
	if series != `{"t":[1791244800,1791244860],"avg":[12.3,null],"max":[20,null],"fail":[0,1]}` {
		t.Fatalf("series %s", series)
	}
	var o struct {
		From, To            int64
		Down, Maint, Paused [][2]int64
		Marks               []int64
	}
	if err := json.Unmarshal([]byte(overlays), &o); err != nil {
		t.Fatal(err)
	}
	// The first outage is clipped to the range and not marked (it began
	// before); the active one ends now.
	if len(o.Down) != 2 || o.Down[0] != [2]int64{cm(0).Unix(), cm(2).Unix()} || o.Down[1] != [2]int64{cm(8).Unix(), cm(9).Unix()} ||
		len(o.Marks) != 1 || o.Marks[0] != cm(8).Unix() || len(o.Maint) != 1 || o.Paused == nil {
		t.Fatalf("overlays %s", overlays)
	}
}

func TestSparkline(t *testing.T) {
	sp := sparkline([]float64{10, 20, -1, 40, 30})
	if sp.Points != "0.0,17.0 25.0,12.0 75.0,2.0 100.0,7.0" || len(sp.Failures) != 1 || sp.Failures[0] != 50 {
		t.Fatalf("%+v", sp)
	}
	if sp.Label != "Latency of the last 5 checks: 10 ms to 40 ms, 1 failed" {
		t.Fatalf("label %q", sp.Label)
	}
	if sp := sparkline([]float64{-1, -1}); sp.Points != "" || sp.Label != "The last 2 checks failed" {
		t.Fatalf("all failed: %+v", sp)
	}
	if sp := sparkline([]float64{5}); sp.Label != "" {
		t.Fatalf("one point: %+v", sp)
	}
}

func TestHistorySummary(t *testing.T) {
	ms := time.Millisecond
	s := store.LatencyStats{Checks: 2880, Failures: 3, Samples: 2877, Current: 120 * ms, Avg: 118 * ms, P95: 310 * ms, Max: 900 * ms}
	iv := store.Intervals{Down: []incident.Span{{From: cm(10), To: cm(17)}, {From: cm(30), To: cm(35)}},
		Maintenance: []incident.Span{{From: cm(40), To: cm(50)}}}
	got := historySummary("the last 24 hours", s, iv, cm(0), cm(1440), cm(1440), incident.Ratio{Up: 99, Den: 100}, incident.Ratio{Up: 1, Den: 1})
	want := "Latency over the last 24 hours: current 120 ms, average 118 ms, p95 310 ms, max 900 ms, from 2880 checks, 3 failed. " +
		"2 outages, down 12m in total. 1 maintenance window. Uptime 99.00%, adjusted 100.00%."
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	s.Approximate = true
	if got := historySummary("the last year", s, store.Intervals{}, cm(0), cm(1440), cm(1440), incident.Ratio{}, incident.Ratio{}); !strings.Contains(got, ", p95 (approximate) 310 ms,") {
		t.Fatalf("approximate: %s", got)
	}
	if got := historySummary("the last hour", store.LatencyStats{}, store.Intervals{}, cm(0), cm(60), cm(60), incident.Ratio{}, incident.Ratio{}); got != "No checks over the last hour. No outages." {
		t.Fatalf("empty: %s", got)
	}
}

// A range holding rolled-up buckets says its p95 is approximate; one of
// raw results only does not.
func TestHistoryTabApproximateP95(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	id := e.addMonitor(t, "API", "https://api.example.com")
	if _, err := e.db.Writer.Exec(`INSERT INTO check_aggregates VALUES (?, 3600, ?, 120, 118, 2, 40, 240, 120, 230)`,
		id, store.FormatTime(time.Now().AddDate(0, 0, -100).Truncate(time.Hour))); err != nil {
		t.Fatal(err)
	}
	year := e.getAs(t, "v1", "GET", "/monitors/"+id+"?tab=history&range=1y").Body.String()
	if !strings.Contains(year, "p95 (approximate)") || !strings.Contains(year, "230 ms") {
		t.Error("1 year: p95 not labelled approximate")
	}
	day := e.getAs(t, "v1", "GET", "/monitors/"+id+"?tab=history&range=24h").Body.String()
	if strings.Contains(day, "approximate") {
		t.Error("24 hours without rolled data: labelled approximate")
	}
}

// The History tab shows the figures, the chart with its data and the
// timeline, and loads uPlot only when there is something to draw.
func TestHistoryTab(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	id := e.addMonitor(t, "API", "https://api.example.com")
	empty := e.getAs(t, "v1", "GET", "/monitors/"+id+"?tab=history").Body.String()
	if !strings.Contains(empty, "No checks were stored in this range") || strings.Contains(empty, "uplot") {
		t.Fatal("empty history: wrong page")
	}

	now := time.Now().Truncate(time.Second)
	tx, err := e.db.Writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 60 {
		r := store.CheckResult{MonitorID: id, CheckedAt: now.Add(-time.Duration(60-i)*time.Minute + 30*time.Second), Duration: time.Duration(100+i) * time.Millisecond, Success: i != 30}
		if err := store.InsertCheckResult(t.Context(), tx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Writer.Exec(`INSERT INTO incidents (id, monitor_id, started_at, ended_at, created_at) VALUES ('i1', ?, ?, ?, ?)`,
		id, store.FormatTime(now.Add(-30*time.Minute)), store.FormatTime(now.Add(-25*time.Minute)), store.FormatTime(now.Add(-30*time.Minute))); err != nil {
		t.Fatal(err)
	}
	body := e.getAs(t, "v1", "GET", "/monitors/"+id+"?tab=history&range=1h").Body.String()
	for _, want := range []string{
		"Over the last 1 hour", "from 60 checks, 1 failed", "1 outage, down 5m in total", "<dt>p95</dt>",
		`data-chart`, `data-series="{&#34;t&#34;:[`, `data-tz="UTC"`, `aria-describedby="history-summary"`,
		`class="tl-down"`, "<title>Down: ", "/static/js/uplot.min.", "/static/js/chart.", "/static/css/uplot.min.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("history tab lacks %q in %s", want, body[strings.Index(body, "history-summary"):][:300])
		}
	}
	if strings.Index(body, "/static/js/uplot.min.") > strings.Index(body, "/static/js/chart.") {
		t.Error("chart.js loads before uPlot")
	}
	bad := e.getAs(t, "v1", "GET", "/monitors/"+id+"?tab=history&from=2026-01-02T00:00&to=2026-01-01T00:00").Body.String()
	if !strings.Contains(bad, "The end must be after the start.") || !strings.Contains(bad, "Over the last 24 hours") {
		t.Error("an invalid custom range is not explained")
	}
	header := e.getAs(t, "v1", "GET", "/fragments/monitors/"+id+"/header").Body.String()
	if !strings.Contains(header, `class="sparkline"`) || !strings.Contains(header, "Latency of the last 30 checks") {
		t.Errorf("header has no sparkline:\n%s", header)
	}
}

// The History tab offers every preset as a link and a custom range as a
// form that keeps what was asked for; the Overview tab shows the last
// 24 hours without a selector, whatever the query says.
func TestRangeSelectorAndOverviewGraph(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	id := e.addMonitor(t, "API", "https://api.example.com")
	now := time.Now().Truncate(time.Second)
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET created_at = ? WHERE id = ?`, store.FormatTime(now.Add(-2*time.Hour)), id); err != nil {
		t.Fatal(err)
	}
	tx, err := e.db.Writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		r := store.CheckResult{MonitorID: id, CheckedAt: now.Add(-time.Duration(20-i) * time.Minute), Duration: 100 * time.Millisecond, Success: true}
		if err := store.InsertCheckResult(t.Context(), tx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	history := e.getAs(t, "v1", "GET", "/monitors/"+id+"?tab=history&range=7d").Body.String()
	for _, p := range []string{"1h", "24h", "7d", "30d", "90d", "1y"} {
		if !strings.Contains(history, `href="/monitors/`+id+`?tab=history&amp;range=`+p+`"`) {
			t.Errorf("no link for %s", p)
		}
	}
	if !strings.Contains(history, `class="range-link" href="/monitors/`+id+`?tab=history&amp;range=7d" aria-current="true"`) ||
		strings.Count(history, `aria-current="true"`) != 1 {
		t.Error("the shown preset is not the only current one")
	}
	for _, want := range []string{`<form class="range-custom" method="get" action="/monitors/` + id + `"`,
		`name="tab" value="history"`, `name="from" type="datetime-local"`, `name="to" type="datetime-local"`, "Over the last 7 days"} {
		if !strings.Contains(history, want) {
			t.Errorf("history tab lacks %q", want)
		}
	}

	custom := e.getAs(t, "v1", "GET", "/monitors/"+id+"?tab=history&from=2026-01-01T08:00&to=2026-01-02T09:30").Body.String()
	for _, want := range []string{`value="2026-01-01T08:00"`, `value="2026-01-02T09:30"`, "Over 1 Jan 2026 08:00 to 2 Jan 2026 09:30 UTC"} {
		if !strings.Contains(custom, want) {
			t.Errorf("custom range lacks %q", want)
		}
	}
	if strings.Contains(custom, `aria-current="true"`) {
		t.Error("a preset is marked current for a custom range")
	}

	overview := e.getAs(t, "v1", "GET", "/monitors/"+id+"?range=7d&from=2026-01-01T00:00&to=2026-01-02T00:00").Body.String()
	for _, want := range []string{"Last 24 hours", "Over the last 24 hours", "data-chart", "/static/js/uplot.min.", `class="timeline"`, "tab=history"} {
		if !strings.Contains(overview, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
	if strings.Contains(overview, "range-custom") {
		t.Error("the overview offers the range selector")
	}
}

// The detail header shows the last 24 hours' uptime, raw and, when it
// differs, adjusted for maintenance; the list leaves it out.
func TestHeaderUptime(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	id := e.addMonitor(t, "API", "https://api.example.com")
	now := time.Now().Truncate(time.Second)
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET created_at = ? WHERE id = ?`, store.FormatTime(now.Add(-2*time.Hour)), id); err != nil {
		t.Fatal(err)
	}
	header := e.getAs(t, "v1", "GET", "/fragments/monitors/"+id+"/header").Body.String()
	if !strings.Contains(header, "Uptime, 24 h") || !strings.Contains(header, "100.00%") {
		t.Errorf("a monitor without outages is not at 100%%:\n%s", header)
	}
	if strings.Contains(header, "adjusted") {
		t.Error("adjusted shown while equal")
	}

	e.addIncident(t, "i1", id, now.Add(-30*time.Minute), ptr(now.Add(-25*time.Minute)), false)
	if _, err := store.CreateMaintenance(t.Context(), e.db, maintenance.Window{Name: "patch", Start: now.Add(-40 * time.Minute), Duration: 20 * time.Minute,
		Recurrence: maintenance.None, ExcludeUptime: true}, now); err != nil {
		t.Fatal(err)
	}
	header = e.getAs(t, "v1", "GET", "/fragments/monitors/"+id+"/header").Body.String()
	if !regexp.MustCompile(`95\.8\d%`).MatchString(header) || !strings.Contains(header, "(100.00% adjusted)") {
		t.Errorf("raw and adjusted uptime missing:\n%s", header)
	}
	if list := e.getAs(t, "v1", "GET", "/fragments/monitors").Body.String(); strings.Contains(list, "adjusted") || !strings.Contains(list, "<dt>Uptime</dt>") {
		t.Errorf("the list shows uptime:\n%s", list)
	}
}

func ptr[T any](v T) *T { return &v }
