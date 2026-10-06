package templates

// Problem is one line of the problem strip.
type Problem struct {
	State string // StateDown, StateFlapping or "tls"
	Text  string
	Href  string
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
	Problems []Problem
	// MoreProblems is how many more there are than are listed.
	MoreProblems int
	Counts       []Count
	Incidents    IncidentListView // the latest, active first
}
