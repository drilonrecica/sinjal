package templates

// IncidentRowView is one incident as the lists show it.
type IncidentRowView struct {
	ID          string
	MonitorID   string
	Monitor     string
	Active      bool
	Started     string // in the instance time zone
	StartedAt   string // RFC 3339, for <time>
	Duration    string // "so far" while active
	Kind        string // of the first failure, "" when unknown
	Summary     string
	Parent      bool // its DOWN was held back by a parent that was down
	Maintenance bool // a maintenance window was in effect
	Flapping    bool // a notification was held back while the monitor flapped
}

// IncidentListView is a list of incidents: all of them, or those of one
// monitor. Fragment is the address that list refreshes itself from.
type IncidentListView struct {
	Fragment    string
	ShowMonitor bool // the monitor's name on each row; not on its own tab
	Rows        []IncidentRowView
	Limit       int // ended incidents shown at most
	More        bool
}

// IncidentEventView is one entry of an incident's timeline.
type IncidentEventView struct {
	Label   string
	Message string
	Time    string
	At      string
	Note    bool
}

// IncidentDetailView is the page of one incident.
type IncidentDetailView struct {
	Incident IncidentRowView
	Events   []IncidentEventView
	Admin    bool
	NoteMax  int
	Error    string
}
