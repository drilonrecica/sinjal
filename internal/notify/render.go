package notify

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/drilonrecica/sinjal/internal/incident"
)

// Kind is what a message is about (docs/36).
type Kind string

const (
	KindDown       Kind = "down"
	KindRecovery   Kind = "recovery"
	KindFlapping   Kind = "flapping"
	KindStable     Kind = "stable"
	KindTLSWarning Kind = "tls_warning"
	KindReminder   Kind = "reminder" // an incident is still open
)

// KindOf is the kind of message for a notification intent.
func KindOf(k incident.IntentKind) Kind {
	switch k {
	case incident.IntentRecovery:
		return KindRecovery
	case incident.IntentFlapping:
		return KindFlapping
	case incident.IntentStable:
		return KindStable
	case incident.IntentTLSWarning:
		return KindTLSWarning
	}
	return KindDown
}

// Severity routes a message to channels (docs/11).
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// SeverityOf is the severity of a kind: recovery and stable are info, TLS
// expiry and flapping warnings, an outage and its reminder critical.
func SeverityOf(k Kind) Severity {
	switch k {
	case KindRecovery, KindStable:
		return SeverityInfo
	case KindTLSWarning, KindFlapping:
		return SeverityWarning
	}
	return SeverityCritical
}

// Bounds on what one message holds, so a long reason cannot swamp a
// channel.
const (
	maxReasonRunes = 200
	maxNameRunes   = 100
	discordTitle   = 256
	discordBody    = 4096
)

// Event is everything a message may say: the safe variable set of
// docs/11. Nothing else reaches a message, so a configured secret or a
// response body cannot leak through a template (there is none).
type Event struct {
	Kind Kind
	Test bool // a simulated incident: the message says so

	MonitorID   string
	MonitorName string
	MonitorType string

	IncidentID    string    // "" when the event belongs to no incident
	IncidentStart time.Time // zero when there is none
	At            time.Time // when it happened: the failure, the recovery, the notice
	Duration      time.Duration
	Reason        string
	Attempts      int
	Latency       *time.Duration // nil when unknown

	CertExpiry time.Time // tls_warning
	DaysLeft   int       // tls_warning

	StatusURL string // the status page, when there is a public one
}

// Message is a rendered notification, before it is shaped for a channel.
// Title is one line; Lines follow it.
type Message struct {
	Event    Event
	Severity Severity
	Title    string
	Lines    []string
}

// Render words an event. Times are shown in loc (the instance time zone,
// UTC when nil).
func Render(e Event, loc *time.Location) Message {
	if loc == nil {
		loc = time.UTC
	}
	name := oneLine(e.MonitorName, maxNameRunes)
	m := Message{Event: e, Severity: SeverityOf(e.Kind)}
	reason := func() {
		if r := oneLine(e.Reason, maxReasonRunes); r != "" {
			m.Lines = append(m.Lines, "Reason: "+r)
		}
	}
	latency := func(label string) {
		if e.Latency != nil {
			m.Lines = append(m.Lines, label+": "+FormatLatency(*e.Latency))
		}
	}
	switch e.Kind {
	case KindRecovery:
		m.Title = name + " recovered"
		m.Lines = append(m.Lines, "Downtime: "+FormatDuration(e.Duration))
		latency("Current latency")
	case KindFlapping:
		m.Title = name + " is flapping"
		m.Lines = append(m.Lines,
			fmt.Sprintf("Repeated state changes detected in the last %s.", windowText(incident.FlapWindow)),
			"Further transition notifications are temporarily suppressed.")
	case KindStable:
		m.Title = name + " is stable again"
		m.Lines = append(m.Lines, fmt.Sprintf("Currently UP. No state changes in the last %s.", windowText(incident.FlapWindow)))
	case KindTLSWarning:
		m.Title = name + " TLS certificate " + expiryText(e.DaysLeft)
		m.Lines = append(m.Lines, "Expiry: "+e.CertExpiry.In(loc).Format("2006-01-02"))
	case KindReminder:
		m.Title = name + " is still DOWN"
		m.Lines = append(m.Lines, "Duration: "+FormatDuration(e.Duration))
		reason()
	default: // KindDown
		m.Title = name + " is DOWN"
		reason()
		if !e.At.IsZero() {
			m.Lines = append(m.Lines, "Failed at: "+e.At.In(loc).Format("2006-01-02 15:04:05 MST"))
		}
		if e.Attempts > 0 {
			m.Lines = append(m.Lines, fmt.Sprintf("Attempts: %d", e.Attempts))
		}
		latency("Last latency")
	}
	if e.Test {
		m.Title = "[TEST] " + m.Title
		m.Lines = append([]string{"This is a simulated Sinjal incident."}, m.Lines...)
	}
	if e.StatusURL != "" {
		m.Lines = append(m.Lines, "Status: "+e.StatusURL)
	}
	return m
}

// Text is the message as plain text: the title, then one line per fact.
func (m Message) Text() string {
	return m.Title + "\n" + strings.Join(m.Lines, "\n")
}

// expiryText is "expires in 14 days", "expires in 1 day", "expires today" or
// "has expired".
func expiryText(days int) string {
	switch {
	case days < 0:
		return "has expired"
	case days == 0:
		return "expires today"
	case days == 1:
		return "expires in 1 day"
	}
	return fmt.Sprintf("expires in %d days", days)
}

// windowText is a whole number of minutes: "10 minutes".
func windowText(d time.Duration) string {
	m := int(d / time.Minute)
	if m == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", m)
}

// FormatDuration is "45s", "4m 17s", "1h 02m" or "2d 03h": two units at most,
// rounded to the second, never negative.
func FormatDuration(d time.Duration) string {
	s := int64(d.Round(time.Second) / time.Second)
	switch {
	case s <= 0:
		return "0s"
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh %02dm", s/3600, s%3600/60)
	}
	return fmt.Sprintf("%dd %02dh", s/86400, s%86400/3600)
}

// FormatLatency is "<1 ms", "74 ms" or "1.5 s".
func FormatLatency(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1 ms"
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}

// oneLine makes a value that came from elsewhere (a monitor name, a failure
// message) safe for one line of any channel: control characters and
// line breaks become spaces, runs of spaces collapse, and it is cut at max
// characters.
func oneLine(s string, max int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }), " ")
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return s
}

// Telegram is the message text: plain, with no parse mode, so nothing in a
// name or reason is read as formatting.
func (m Message) Telegram() string { return m.Text() }

// Email is the subject and the plain-text body; plain text is what an email
// must be readable as (docs/36).
func (m Message) Email() (subject, body string) {
	return m.Title, m.Text() + "\n"
}

// Discord is the JSON body of a webhook call: one lightweight embed. No
// mention is allowed to fire, whatever the names and reasons say.
func (m Message) Discord() ([]byte, error) {
	title := truncate(m.Title, discordTitle)
	desc := truncate(strings.Join(m.Lines, "\n"), discordBody)
	embed := map[string]any{"title": title, "description": desc, "color": discordColor(m)}
	if !m.Event.At.IsZero() {
		embed["timestamp"] = m.Event.At.UTC().Format(time.RFC3339)
	}
	return json.Marshal(map[string]any{
		"embeds":           []any{embed},
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
}

func discordColor(m Message) int {
	switch {
	case m.Event.Test:
		return 0x8B8D98 // grey: simulated
	case m.Event.Kind == KindRecovery || m.Event.Kind == KindStable:
		return 0x30A46C
	case m.Severity == SeverityWarning:
		return 0xF5A524
	}
	return 0xE5484D
}

func truncate(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// webhookEvent names the event of a payload (docs/36).
var webhookEvent = map[Kind]string{
	KindDown: "monitor.down", KindRecovery: "monitor.recovered", KindFlapping: "monitor.flapping",
	KindStable: "monitor.stable", KindTLSWarning: "monitor.tls_warning", KindReminder: "monitor.reminder",
}

type (
	webhookPayload struct {
		Event         string           `json:"event"`
		Test          bool             `json:"test,omitempty"`
		Severity      Severity         `json:"severity"`
		Timestamp     string           `json:"timestamp,omitempty"`
		Monitor       webhookMonitor   `json:"monitor"`
		Incident      *webhookIncident `json:"incident"`
		Reason        string           `json:"reason,omitempty"`
		Attempts      int              `json:"attempts,omitempty"`
		LatencyMS     *int64           `json:"latency_ms,omitempty"`
		Certificate   *webhookCert     `json:"certificate,omitempty"`
		StatusPageURL string           `json:"status_page_url,omitempty"`
	}
	webhookMonitor struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	webhookIncident struct {
		ID              string `json:"id"`
		StartedAt       string `json:"started_at,omitempty"`
		DurationSeconds *int64 `json:"duration_seconds"`
	}
	webhookCert struct {
		ExpiresAt     string `json:"expires_at"`
		DaysRemaining int    `json:"days_remaining"`
	}
)

// Webhook is the stable JSON payload of docs/36. Times are RFC 3339 in
// UTC, for machines; the incident is null when the event has none.
func (m Message) Webhook() ([]byte, error) {
	e := m.Event
	p := webhookPayload{
		Event: webhookEvent[e.Kind], Test: e.Test, Severity: m.Severity,
		Monitor:       webhookMonitor{ID: e.MonitorID, Name: oneLine(e.MonitorName, maxNameRunes), Type: e.MonitorType},
		Reason:        oneLine(e.Reason, maxReasonRunes),
		Attempts:      e.Attempts,
		StatusPageURL: e.StatusURL,
	}
	if p.Event == "" {
		p.Event = webhookEvent[KindDown]
	}
	if !e.At.IsZero() {
		p.Timestamp = e.At.UTC().Format(time.RFC3339)
	}
	if e.IncidentID != "" {
		inc := &webhookIncident{ID: e.IncidentID}
		if !e.IncidentStart.IsZero() {
			inc.StartedAt = e.IncidentStart.UTC().Format(time.RFC3339)
		}
		if e.Kind == KindRecovery || e.Kind == KindReminder {
			s := int64(e.Duration.Round(time.Second) / time.Second)
			inc.DurationSeconds = &s
		}
		p.Incident = inc
	}
	if e.Latency != nil {
		ms := e.Latency.Milliseconds()
		p.LatencyMS = &ms
	}
	if e.Kind == KindTLSWarning {
		p.Certificate = &webhookCert{ExpiresAt: e.CertExpiry.UTC().Format(time.RFC3339), DaysRemaining: e.DaysLeft}
	}
	return json.MarshalIndent(p, "", "  ")
}
