package templates

// MonitorDetailView is the monitor detail page (docs/03 "Monitor detail").
// Everything is preformatted by the web package, which also decides what a
// viewer may see: Config and Failures are already reduced for viewers.
type MonitorDetailView struct {
	Monitor  MonitorView
	Admin    bool
	Paused   bool
	Tab      string // one of the DetailTabs keys
	Overview []Fact
	Config   []ConfigGroup
	Failures []FailureView
	History  HistoryView
	// Incidents is the Incidents tab: this monitor's incidents.
	Incidents IncidentListView
}

// Fact is one label/value pair.
type Fact struct {
	Label string
	Value string
}

// ConfigGroup is one titled block of the Configuration tab.
type ConfigGroup struct {
	Title string
	Facts []Fact
}

// FailureView is one failed check on the Diagnostics tab. Message and
// Snippet are empty for viewers.
type FailureView struct {
	When     string // "2026-10-06 12:00:00 UTC"
	WhenAt   string // RFC 3339
	Kind     string // "Timeout", "HTTP status", …
	Status   string // protocol status, e.g. "503"
	Message  string
	Snippet  string
	Duration string
}

// DetailTab is one tab of the detail page.
type DetailTab struct {
	Key   string
	Label string
}

// DetailTabs lists the tabs in display order; the first is the default.
var DetailTabs = []DetailTab{
	{"overview", "Overview"},
	{"history", "History"},
	{"incidents", "Incidents"},
	{"configuration", "Configuration"},
	{"diagnostics", "Diagnostics"},
}

// IsDetailTab reports whether key names a tab.
func IsDetailTab(key string) bool {
	for _, t := range DetailTabs {
		if t.Key == key {
			return true
		}
	}
	return false
}

// tabURL is the address of a tab; the default tab has none of its own.
func tabURL(id, key string) string {
	if key == DetailTabs[0].Key {
		return "/monitors/" + id
	}
	return "/monitors/" + id + "?tab=" + key
}

// DeleteConfirmView is the confirmation step of deleting a monitor.
type DeleteConfirmView struct {
	ID   string
	Name string
}

// detailAssets are the detail page's styles and scripts; the History tab
// adds uPlot and the chart glue, which no other page loads.
func detailAssets(v MonitorDetailView) ([]string, []string) {
	// charts.css also styles the header's sparkline, on every tab.
	styles := []string{"css/monitors.css", "css/monitor_detail.css", "css/charts.css"}
	if v.Tab == "incidents" {
		styles = append(styles, "css/incidents.css")
	}
	if (v.Tab != "history" && v.Tab != "overview") || !v.History.HasData {
		return styles, LiveScripts
	}
	return append(styles, "css/uplot.min.css"),
		append(LiveScripts[:len(LiveScripts):len(LiveScripts)], "js/uplot.min.js", "js/chart.js")
}
