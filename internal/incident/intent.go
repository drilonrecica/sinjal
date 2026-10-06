package incident

import "time"

// IntentKind is what a notification would be about.
type IntentKind string

const (
	IntentDown       IntentKind = "down"        // an incident opened, or flapping ended while down
	IntentRecovery   IntentKind = "recovery"    // a check closed an incident
	IntentFlapping   IntentKind = "flapping"    // the monitor started flapping
	IntentStable     IntentKind = "stable"      // flapping ended while not down
	IntentTLSWarning IntentKind = "tls_warning" // a certificate is close to expiry
)

// Reason is why an intent is not to be delivered; the empty Reason means
// that it is.
type Reason string

const (
	ByMaintenance Reason = "maintenance" // a maintenance window that suppresses notifications
	ByParent      Reason = "parent"      // the parent monitor is down
	ByFlapping    Reason = "flapping"    // the monitor is flapping
)

// Conditions is what holds for a monitor at the moment of an intent.
type Conditions struct {
	Maintenance bool // inside a window that suppresses notifications
	ParentDown  bool
	Flapping    bool
}

// Suppression decides whether an intent is delivered. When several
// conditions apply, one reason is recorded: maintenance, then parent, then
// flapping.
//
//	maintenance  suppresses every kind
//	parent down  suppresses every kind except tls_warning, which is about
//	             the monitor's own certificate
//	flapping     suppresses down and recovery; the flapping notice itself
//	             and what ends it (stable, down) are what flapping sends
func Suppression(kind IntentKind, c Conditions) Reason {
	switch {
	case c.Maintenance:
		return ByMaintenance
	case c.ParentDown && kind != IntentTLSWarning:
		return ByParent
	case c.Flapping && (kind == IntentDown || kind == IntentRecovery):
		return ByFlapping
	}
	return ""
}

// Intent is a notification the result processor decided on. Delivery is
// the dispatcher's job; a suppressed intent is never delivered.
type Intent struct {
	Kind       IntentKind
	MonitorID  string
	IncidentID string // "" when the intent belongs to no incident
	At         time.Time
	Suppressed Reason
}
