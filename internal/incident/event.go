package incident

// Event types of an incident's timeline (incident_events.event_type).
const (
	EventDetected     = "detected"      // the first failed check of the outage
	EventDeclaredDown = "declared_down" // the failure threshold was met
	EventRecovered    = "recovered"     // a check closed the incident
	EventPaused       = "paused"        // pausing the monitor closed the incident
	// A notification about the incident was decided against; the message
	// is the intent's kind and the reason ("down: flapping").
	EventNotificationSuppressed = "notification_suppressed"
)
