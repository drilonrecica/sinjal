package templates

// Problem is one line of the problem strip.
type Problem struct {
	State string // StateDown, StateFlapping, "channel", "tls" or "system"
	Text  string
	Href  string // "" for a line without a page of its own
}

// RecentEvent is one line of Recent events: an incident timeline entry.
type RecentEvent struct {
	Label        string // "Declared down", "Recovered", …
	Monitor      string
	MonitorHref  string
	IncidentHref string
	Time         string // in the instance time zone
	At           string // RFC 3339
}

// Count is one figure of the summary.
type Count struct {
	Label string
	Value int
	State string // the state it counts, "" for the total
}

// OverviewView is the Overview page (docs/03 "Overview").
type OverviewView struct {
	Admin    bool
	Monitors int // how many exist
	// Sentence is the status in one sentence; SentenceState (up, down or
	// flapping) colours the hero's Ambient light.
	Sentence      string
	SentenceState string
	Problems      []Problem
	// MoreProblems is how many more there are than are listed.
	MoreProblems int
	Counts       []Count
	// Maintenance is the windows in effect and those beginning soon.
	Maintenance []MaintenanceNote
	Incidents   IncidentListView // the latest, active first
	Events      []RecentEvent    // the latest incident timeline entries, newest first
}
