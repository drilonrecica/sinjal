package templates

import (
	"bytes"
	"context"
	"regexp"
	"testing"

	"github.com/a-h/templ"
)

var (
	scriptTagRe = regexp.MustCompile(`(?i)<script\b[^>]*>`)
	// Inline code the CSP blocks besides <script>: style elements and
	// attributes, event handler attributes, javascript: URLs.
	inlineCodeRe = regexp.MustCompile(`(?i)<style[\s>]|\sstyle=|\son[a-z]+=|javascript:`)
)

// cspViolation returns the first construct in html that the CSP would block.
func cspViolation(html string) string {
	for _, tag := range scriptTagRe.FindAllString(html, -1) {
		if !regexp.MustCompile(`(?i)\ssrc=`).MatchString(tag) {
			return tag
		}
	}
	return inlineCodeRe.FindString(html)
}

// TestPagesNeedNoInlineCode renders every full page and fails on anything
// the CSP (middleware.ContentSecurityPolicy) would block, so a template
// change cannot silently break a page in the browser. Add new pages here.
func TestPagesNeedNoInlineCode(t *testing.T) {
	page := NewPage("Test", "", "")
	page.CSRFToken = "token"
	for _, bad := range []string{`<script>x()</script>`, `<script type="module">`, `<p style="x">`, `<style>`, `<a onclick="x">`, `<a href="javascript:x">`} {
		if cspViolation(bad) == "" {
			t.Fatalf("checker misses %s", bad)
		}
	}
	if v := cspViolation(`<script src="/static/a.js" defer></script>`); v != "" {
		t.Fatalf("checker flags an external script: %s", v)
	}

	pages := map[string]templ.Component{
		"shell":       PlaceholderPage(page, Sections[0]),
		"setup":       Setup(page, SetupForm{Token: "t", Errors: map[string]string{"login": "x", "form": "y"}}, 12),
		"authMessage": AuthMessage(page, "Heading", "Message"),
		"login":       Login(page, LoginForm{Login: "a", Next: "/x", Error: "e", Passkey: true}),
		"loginTOTP":   LoginTOTP(page, LoginTOTPForm{Challenge: "c", Next: "/x", Error: "e"}),
		"reauth":      Reauth(page, ReauthForm{Login: "a", Next: "/x", Error: "e", TOTP: true, Passkey: true}),
		"settings":    SettingsAuth(page, SettingsAuthView{TOTPEnabled: true, Passkeys: []PasskeyView{{ID: "p1", Label: "Laptop", Added: "2026-10-06", LastUsed: "Never used"}}}),
		"settingsViewers": SettingsAuth(page, SettingsAuthView{MinPassword: 12, Viewers: []ViewerView{{ID: "v1", Login: "vera", Added: "2026-10-06"}, {ID: "v2", Login: "vic", Disabled: true, Added: "2026-10-06"}},
			ViewerForm: ViewerForm{Login: "x", Errors: map[string]string{"login": "bad", "password": "bad", "confirm": "bad"}}}),
		"accountPassword": AccountPassword(page, PasswordForm{Changed: true, Errors: map[string]string{"password": "bad", "confirm": "bad"}}, 12),
		"system":          SettingsSystem(page, SystemView{Audit: []AuditRow{{When: "2026-10-06 12:00:00", Actor: "admin", Event: "auth.login_succeeded", Object: "user u1", Details: "client_ip=1.2.3.4"}, {When: "2026-10-06 11:00:00", Event: "auth.login_failed"}}, OlderPath: "/settings/system?before=5"}),
		"systemEmpty":     SettingsSystem(page, SystemView{}),
		"monitorRow":      MonitorRow(testMonitor),
		"monitorHeader":   MonitorHeader(testMonitor),
		"live":            Live(),
		"monitorList":     MonitorList(page, MonitorListView{Admin: true, Monitors: []MonitorView{testMonitor}}),
		"monitorListNone": MonitorList(page, MonitorListView{Admin: true}),
		"monitorForm":     MonitorFormPage(page, testMonitorForm),
		"monitorRowsNone": MonitorRows(nil),
		"deleteConfirm":   MonitorDeleteConfirm(page, DeleteConfirmView{ID: "m1", Name: "API"}),
		"monitorFormNew":  MonitorFormPage(page, MonitorForm{Enabled: true, Assertions: []AssertionField{{}}}),
		"settingsOff":     SettingsAuth(page, SettingsAuthView{PasskeysUnavailable: "SINJAL_BASE_URL is not set."}),
		"maintenanceList": MaintenanceList(page, MaintenanceListView{Admin: true, Zone: "UTC",
			Active:   []MaintenanceRow{{ID: "w1", Name: "Now", Schedule: "Daily at 02:00 for 1 h", Scope: "All monitors", When: "Until 03:00", Suppress: true, ExcludeUptime: true}},
			Upcoming: []MaintenanceRow{{ID: "w2", Name: "Later", Scope: "API, tag prod"}}, Past: []MaintenanceRow{{ID: "w3", Name: "Over"}}}),
		"maintenanceNone": MaintenanceList(page, MaintenanceListView{Zone: "UTC"}),
		"maintenanceForm": MaintenanceFormPage(page, MaintenanceForm{ID: "w1", Name: "x", Recurrence: "weekly", Zone: "UTC",
			Monitors: []ScopeOption{{Value: "m1", Label: "API", Checked: true}}, Tags: []ScopeOption{{Value: "prod", Label: "prod"}},
			Errors: map[string]string{"name": "x", "starts_at": "x", "duration": "x", "weekdays": "x", "scope": "x", "form": "x"}}),
		"maintenanceNew":    MaintenanceFormPage(page, MaintenanceForm{Recurrence: "none", ScopeAll: true, Suppress: true}),
		"maintenanceDelete": MaintenanceDeleteConfirm(page, DeleteConfirmView{ID: "w1", Name: "Now"}),
		"incidentsList":     IncidentsPage(page, testIncidents),
		"incidentsNone":     IncidentsPage(page, IncidentListView{Fragment: "/fragments/incidents", ShowMonitor: true}),
		"incidentDetail": IncidentDetail(page, IncidentDetailView{Incident: testIncidents.Rows[0], Admin: true, NoteMax: 1000, Error: "e",
			Events: []IncidentEventView{{Label: "First failure", Message: "<b>x</b>", Time: "t", At: "a"}, {Label: "Note", Message: "n", Time: "t", At: "a", Note: true}}}),
		"incidentDetailViewer": IncidentDetail(page, IncidentDetailView{Incident: testIncidents.Rows[1]}),
		"totpSetup":            TOTPSetup(page, TOTPSetupView{Secret: "AAAA BBBB", URI: "otpauth://totp/x", QR: "data:image/png;base64,AAAA", Pending: "p", Error: "e"}),
	}
	for _, tab := range DetailTabs {
		v := MonitorDetailView{Monitor: testMonitor, Admin: true, Tab: tab.Key,
			Overview: []Fact{{"Created", "2026-10-06"}}, Config: []ConfigGroup{{"Checking", []Fact{{"Interval", "30 s"}}}},
			Failures: []FailureView{{When: "w", Kind: "Timeout", Message: "m", Snippet: "<b>x</b>"}}}
		pages["detail-"+tab.Key] = MonitorDetail(page, v)
		v.Admin, v.Paused = false, true
		pages["detailViewer-"+tab.Key] = MonitorDetail(page, v)
	}
	incidentsTab := MonitorDetailView{Monitor: testMonitor, Tab: "incidents", Incidents: testIncidents}
	pages["detail-incidents-rows"] = MonitorDetail(page, incidentsTab)
	withData := MonitorDetailView{Monitor: testMonitor, Tab: "history", History: HistoryView{Range: "the last 24 hours", RangeError: "x",
		HasData: true, Summary: "s", Stats: []Fact{{"p95", "1 ms"}}, Uptime: "99.00%", Adjusted: "100.00%",
		Series: `{"t":[1],"avg":[1],"max":[1],"fail":[0]}`, Overlays: `{"from":0,"to":1,"down":[],"maint":[],"paused":[],"marks":[]}`, Zone: "UTC",
		Timeline: []TimelineSegment{{X: 0, W: 500, Class: "up", Title: "Up"}, {X: 500, W: 500, Class: "down", Title: "Down"}}, From: "a", To: "b",
		Selector: true, MonitorID: "m1", FromValue: "2026-10-06T08:00", ToValue: "2026-10-06T09:00", MaxValue: "2026-10-06T09:00",
		Presets: []RangeOption{{Label: "1 hour", Href: "/monitors/m1?tab=history&range=1h", Current: true}, {Label: "24 hours", Href: "/monitors/m1?tab=history&range=24h"}}}}
	withData.Monitor.Sparkline = Sparkline{Points: "0,1 100,2", Failures: []float64{50}, Label: "l"}
	pages["detail-history-data"] = MonitorDetail(page, withData)
	for name, c := range pages {
		var buf bytes.Buffer
		if err := c.Render(context.Background(), &buf); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m := cspViolation(buf.String()); m != "" {
			t.Errorf("%s contains CSP-blocked inline code %q", name, m)
		}
	}
}

var testIncidents = IncidentListView{Fragment: "/fragments/incidents", ShowMonitor: true, Limit: 100, More: true,
	Rows: []IncidentRowView{
		{ID: "i1", MonitorID: "m1", Monitor: "API", Active: true, Started: "10:00 UTC", StartedAt: "2026-10-06T10:00:00Z", Duration: "5m", Summary: "status 503", Parent: true, Maintenance: true},
		{ID: "i2", MonitorID: "m1", Monitor: "API", Started: "09:00 UTC", StartedAt: "2026-10-06T09:00:00Z", Duration: "2m"},
	}}
