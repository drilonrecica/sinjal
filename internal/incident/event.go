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
	// The DOWN notification held back by the parent or by maintenance was
	// decided after all, the monitor still being down when that ended.
	EventNotificationResumed = "notification_resumed"
	// A notification about the incident reached a channel; the message is
	// the kind and the channel ("down via Ops chat").
	EventNotificationSent = "notification_sent"
	// A notification was given up for a channel: every attempt failed, or
	// the incident ended before a retry. The message adds the last error.
	EventNotificationFailed = "notification_failed"
	// EventManualNote is a note an admin attached to the incident.
	EventManualNote = "manual_note"
)
