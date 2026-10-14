package web

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/monitor/httpcheck"
	"github.com/drilonrecica/sinjal/internal/recent"
	"github.com/drilonrecica/sinjal/web/templates"
)

func TestBeatBar(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, loc)
	checks := []recent.Check{ // newest first, as recent.Store.Newest gives them
		{At: now.Add(-30 * time.Second).Unix(), MS: 128, OK: true},
		{At: now.Add(-time.Minute).Unix(), MS: 5000, Kind: httpcheck.KindTimeout},
		{At: now.Add(-25 * time.Hour).Unix(), MS: 0, OK: true},
		{At: now.Add(-26 * time.Hour).Unix(), MS: 40, Kind: "something new"},
	}
	b := beatBar("m1", checks, barKind{6, true}, newBeatClock(now, loc))

	want := []templates.Beat{
		{Result: templates.BeatUp, Tip: "11:59:30 · 128 ms · Up"},
		{Result: templates.BeatDown, Tip: "11:59:00 · 5.0 s · Down: Timeout"},
		{Result: templates.BeatUp, Tip: "Tue 13 Oct, 11:00:00 · <1 ms · Up"},
		{Result: templates.BeatDown, Tip: "Tue 13 Oct, 10:00:00 · 40 ms · Down"},
		{}, {},
	}
	if len(b.Beats) != len(want) {
		t.Fatalf("%d beats, want %d (padded)", len(b.Beats), len(want))
	}
	for i := range want {
		if b.Beats[i] != want[i] {
			t.Errorf("beat %d = %+v, want %+v", i, b.Beats[i], want[i])
		}
	}
	if b.Newest != checks[0].At || !b.Focusable || b.TipsHref != "" {
		t.Fatalf("newest %d focusable %v tips %q", b.Newest, b.Focusable, b.TipsHref)
	}
	if want := "Last 4 checks: 2 up, 2 down. Latest: up at 11:59:30."; b.Label != want {
		t.Fatalf("label %q, want %q", b.Label, want)
	}
}

func TestBeatBarWithoutChecks(t *testing.T) {
	b := beatBar("m1", nil, barKind{3, false}, newBeatClock(time.Now(), time.UTC))
	if len(b.Beats) != 3 || b.Beats[0] != (templates.Beat{}) || b.Newest != 0 || b.TipsHref != "" || b.Label != "No checks yet." {
		t.Fatalf("%+v", b)
	}
}

func TestBeatBarKeepsTheNewestN(t *testing.T) {
	now := time.Now()
	checks := make([]recent.Check, 10)
	for i := range checks {
		checks[i] = recent.Check{At: now.Unix() - int64(i), OK: true}
	}
	b := beatBar("m1", checks, barKind{4, false}, newBeatClock(now, time.UTC))
	if len(b.Beats) != 4 || b.Label[:13] != "Last 4 checks" {
		t.Fatalf("%d beats, label %q", len(b.Beats), b.Label)
	}
	// A row's beats carry no tips; the bar says where to fetch them.
	if b.Beats[0] != (templates.Beat{Result: templates.BeatUp}) || b.TipsHref != "/fragments/monitors/m1/beats?until="+strconv.FormatInt(now.Unix(), 10) {
		t.Fatalf("row bar: %+v, tips %q", b.Beats[0], b.TipsHref)
	}
}

// The list rows and the detail header draw their beat bars from the
// engine's memory, and the Overview tab shows the 90-day strip.
func TestBeatBarsOnThePages(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	id := e.addMonitor(t, "API", "https://api.example.com")
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	e.engine.Recent().Push(id, recent.Check{At: at.Unix(), MS: 5000, Kind: httpcheck.KindTimeout})
	e.engine.Recent().Push(id, recent.Check{At: at.Add(30 * time.Second).Unix(), MS: 128, OK: true})
	tip := `data-tip="` + at.UTC().Format("15:04:05") + ` · 5.0 s · Down: Timeout"`
	newest := strconv.FormatInt(at.Unix()+30, 10)

	for _, path := range []string{"/monitors", "/fragments/monitors", "/fragments/monitors/" + id + "/row"} {
		body := e.getAs(t, "v1", "GET", path).Body.String()
		if !strings.Contains(body, `data-newest="`+newest+`" role="img" aria-label="Last 2 checks: 1 up, 1 down.`) ||
			!strings.Contains(body, `data-tips="/fragments/monitors/`+id+`/beats?until=`+newest+`"><i data-b="up"></i><i data-b="down"></i><i></i>`) {
			t.Errorf("%s: no row beat bar with both checks", path)
		}
		if strings.Contains(body, "data-tip=") || strings.Contains(body, `tabindex="0"><i`) {
			t.Errorf("%s: the row bar carries tips or is focusable", path)
		}
	}

	// A third check arrives after the page was drawn: the tips still line
	// up with the beats on the page.
	e.engine.Recent().Push(id, recent.Check{At: at.Unix() + 60, MS: 90, OK: true})
	res := e.getAs(t, "v1", "GET", "/fragments/monitors/"+id+"/beats?until="+newest)
	want := `["` + at.Add(30*time.Second).UTC().Format("15:04:05") + ` · 128 ms · Up","` + at.UTC().Format("15:04:05") + ` · 5.0 s · Down: Timeout"]` + "\n"
	if res.Code != 200 || res.Header().Get("Content-Type") != "application/json" || res.Body.String() != want {
		t.Errorf("tips: %d %q %s, want %s", res.Code, res.Header().Get("Content-Type"), res.Body.String(), want)
	}
	if res := e.getAs(t, "v1", "GET", "/fragments/monitors/"+id+"/beats?until=soon"); res.Code != 400 {
		t.Errorf("bad until: %d", res.Code)
	}
	header := e.getAs(t, "v1", "GET", "/fragments/monitors/"+id+"/header").Body.String()
	if !strings.Contains(header, tip) || !strings.Contains(header, `tabindex="0"`) || strings.Count(header, "<i") != detailBeats {
		t.Errorf("header: no focusable bar of %d slots", detailBeats)
	}

	overview := e.getAs(t, "v1", "GET", "/monitors/"+id).Body.String()
	if !strings.Contains(overview, "<h3>Last 90 days</h3>") || strings.Count(overview, "<li data-day=") != stripDays ||
		!strings.Contains(overview, `<ol class="day-bars" role="img" aria-label="Last 90 days: `) {
		t.Error("the Overview tab has no 90-day strip")
	}
}
