package templates

// MaintenanceListView is the Maintenance page: windows in effect now,
// those still to come and those that are over. Times are already in the
// instance time zone, named in Zone.
type MaintenanceListView struct {
	Admin    bool
	Zone     string
	Active   []MaintenanceRow
	Upcoming []MaintenanceRow
	Past     []MaintenanceRow
}

// Empty reports whether there are no windows at all.
func (v MaintenanceListView) Empty() bool {
	return len(v.Active)+len(v.Upcoming)+len(v.Past) == 0
}

// MaintenanceRow is one window in the list.
type MaintenanceRow struct {
	ID            string
	Name          string
	Schedule      string // "Daily at 02:00 for 2 h"
	Scope         string // "All monitors", or the monitors and tags
	When          string // "Until …", "Next …", "Ended …"
	WhenAt        string // RFC 3339 of that moment, for <time datetime>
	Suppress      bool
	ExcludeUptime bool
}

// MaintenanceForm is the state of the create/edit maintenance page. Values
// are kept as typed so a rejected one is shown back as entered.
type MaintenanceForm struct {
	ID              string // "" while creating
	Name            string
	StartsAt        string // datetime-local, in the instance time zone
	DurationHours   string
	DurationMinutes string
	Recurrence      string // "none", "daily" or "weekly"
	Weekdays        [7]bool
	Suppress        bool
	ExcludeUptime   bool
	ScopeAll        bool
	Monitors        []ScopeOption
	Tags            []ScopeOption
	Zone            string
	// Errors maps a field name to its message; "form" holds a failure not
	// tied to one field.
	Errors map[string]string
}

// ScopeOption is one monitor or tag that a window can cover.
type ScopeOption struct {
	Value   string
	Label   string
	Checked bool
}

// Editing reports whether the form edits an existing window.
func (f MaintenanceForm) Editing() bool { return f.ID != "" }

// Action is where the form posts.
func (f MaintenanceForm) Action() string {
	if f.Editing() {
		return "/maintenance/" + f.ID
	}
	return "/maintenance"
}

// WeekdayOrder lists weekdays Monday first, as time.Weekday numbers.
var WeekdayOrder = []int{1, 2, 3, 4, 5, 6, 0}

// WeekdayNames are the short names by time.Weekday number.
var WeekdayNames = [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

var maintenanceFieldOrder = []string{"name", "starts_at", "duration", "recurrence", "weekdays", "scope"}

var maintenanceLabels = map[string]string{
	"name": "Name", "starts_at": "Starts", "duration": "Duration", "recurrence": "Repeats",
	"weekdays": "Weekdays", "scope": "Monitors",
}

// summary lists the errors in page order.
func (f MaintenanceForm) summary() []summaryItem {
	var out []summaryItem
	for _, k := range maintenanceFieldOrder {
		if msg, ok := f.Errors[k]; ok {
			out = append(out, summaryItem{k, maintenanceLabels[k], msg})
		}
	}
	return out
}
