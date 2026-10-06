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
		"totpSetup":         TOTPSetup(page, TOTPSetupView{Secret: "AAAA BBBB", URI: "otpauth://totp/x", QR: "data:image/png;base64,AAAA", Pending: "p", Error: "e"}),
	}
	for _, tab := range DetailTabs {
		v := MonitorDetailView{Monitor: testMonitor, Admin: true, Tab: tab.Key,
			Overview: []Fact{{"Created", "2026-10-06"}}, Config: []ConfigGroup{{"Checking", []Fact{{"Interval", "30 s"}}}},
			Failures: []FailureView{{When: "w", Kind: "Timeout", Message: "m", Snippet: "<b>x</b>"}}}
		pages["detail-"+tab.Key] = MonitorDetail(page, v)
		v.Admin, v.Paused = false, true
		pages["detailViewer-"+tab.Key] = MonitorDetail(page, v)
	}
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
