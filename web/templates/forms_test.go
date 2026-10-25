package templates

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func renderString(t *testing.T, c templ.Component) string {
	t.Helper()
	var b strings.Builder
	if err := c.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// testStatusPageForm is an unlisted page being edited, with an error on
// every kind of field, a group and two monitors.
var testStatusPageForm = StatusPageForm{ID: "s1", Title: "Main", Slug: "main", Visibility: "unlisted", Theme: "daylight", Accent: "#3e67a8",
	IncidentDays: "30", HasPassword: true, HasToken: true, Logo: "0123456789abcdef0123456789abcdef.png", Groups: "Web", GroupNames: []string{"Web"}, Hosts: "status.example.com", Origin: "https://x",
	Monitors: []StatusPageMonitorRow{{ID: "m1", Name: "API", Show: true, DisplayName: "Our API", Group: "Web", Order: "1", ShowLatency: true}, {ID: "m2", Name: "Site"}},
	Errors: map[string]string{"form": "f", "title": "x", "slug": "x", "visibility": "x", "password": "x", "accent": "x", "incident_days": "x",
		"groups": "x", "monitors": "x", "hosts": "x", "logo": "x", "m_name_m1": "x", "m_group_m1": "x", "m_order_m1": "x"}}

var (
	inputRe = regexp.MustCompile(`<input\b[^>]*>`)
	idRe    = regexp.MustCompile(`\bid="([^"]+)"`)
	forRe   = regexp.MustCompile(`<label for="([^"]+)"`)
)

// Every visible input has a label (docs/04 "Forms": no placeholder-only
// labels). Hidden inputs carry no UI.
func TestEveryInputHasALabel(t *testing.T) {
	page := NewPage("t", "", "")
	signedIn := page
	signedIn.Admin = true
	views := map[string]string{
		"setup":        renderString(t, Setup(page, SetupForm{Token: "t"}, 12)),
		"login":        renderString(t, Login(page, LoginForm{})),
		"loginTOTP":    renderString(t, LoginTOTP(page, LoginTOTPForm{Challenge: "c"})),
		"reauth":       renderString(t, Reauth(page, ReauthForm{Login: "admin", TOTP: true})),
		"settings":     renderString(t, SettingsAuth(signedIn, SettingsAuthView{MinPassword: 12})),
		"totpSetup":    renderString(t, TOTPSetup(signedIn, TOTPSetupView{Pending: "p"})),
		"account":      renderString(t, AccountPassword(signedIn, PasswordForm{}, 12)),
		"monitor":      renderString(t, MonitorFormPage(signedIn, testMonitorForm)),
		"profile":      renderString(t, ProfileFormPage(signedIn, testProfileForm)),
		"statusPage":   renderString(t, StatusPageFormPage(signedIn, testStatusPageForm)),
		"pagePassword": renderString(t, PublicPasswordPage(PublicPasswordView{Title: "P", Action: "/status/p", Error: "e", CSRFToken: "t"}, "daylight")),
		"channel":      renderString(t, ChannelFormPage(signedIn, ChannelForm{ID: "c1", Type: "smtp", Values: map[string]string{}, SecretSet: map[string]bool{"password": true}})),
	}
	for name, html := range views {
		labelled := map[string]bool{}
		for _, m := range forRe.FindAllStringSubmatch(html, -1) {
			labelled[m[1]] = true
		}
		for _, in := range inputRe.FindAllString(html, -1) {
			if strings.Contains(in, `type="hidden"`) || strings.Contains(in, " hidden") {
				continue
			}
			m := idRe.FindStringSubmatch(in)
			if m == nil || !labelled[m[1]] {
				t.Errorf("%s: input without a label: %s", name, in)
			}
		}
	}
}

// A failed form ties the generic alert to its fields.
func TestFormErrorIsAssociated(t *testing.T) {
	page := NewPage("t", "", "")
	views := map[string]string{
		"login":     renderString(t, Login(page, LoginForm{Error: "Wrong."})),
		"loginTOTP": renderString(t, LoginTOTP(page, LoginTOTPForm{Challenge: "c", Error: "Wrong."})),
		"reauth":    renderString(t, Reauth(page, ReauthForm{Login: "a", Error: "Wrong."})),
	}
	for name, html := range views {
		if !strings.Contains(html, `id="form-error" class="auth-alert" role="alert"`) {
			t.Errorf("%s: no alert with id form-error", name)
		}
		if n := strings.Count(html, `aria-describedby="form-error"`); n == 0 {
			t.Errorf("%s: no field is described by the alert", name)
		}
		if !strings.Contains(html, `aria-invalid="true"`) {
			t.Errorf("%s: no field is marked invalid", name)
		}
	}
	// Without an error nothing refers to a missing element.
	if html := renderString(t, Login(page, LoginForm{})); strings.Contains(html, "form-error") {
		t.Error("an error-free login form refers to form-error")
	}
}

// The settings navigation marks the current page in text-visible markup and
// hides admin-only pages from viewers.
func TestSettingsNavigation(t *testing.T) {
	admin := NewPage("t", "", "")
	admin.Admin = true
	html := renderString(t, SettingsSystem(admin, SystemView{}))
	if !strings.Contains(html, `href="/settings/system" aria-current="page"`) || !strings.Contains(html, `href="/settings/authentication"`) {
		t.Errorf("admin navigation: %s", html)
	}
	viewer := NewPage("t", "", "")
	html = renderString(t, AccountPassword(viewer, PasswordForm{}, 12))
	if strings.Contains(html, "/settings/system") || strings.Contains(html, "/settings/authentication") {
		t.Error("a viewer's navigation shows admin pages")
	}
}

// testMonitorForm exercises every optional part of the monitor form.
var testMonitorForm = MonitorForm{
	ID: "m1", Type: "http", Name: "API <b>", URL: "https://example.com", Method: "POST", Auth: "basic", HasBasic: true,
	SecretHeaders: []string{"X-Api-Key"}, Parents: []Option{{"m2", "DB"}}, Parent: "m2",
	Assertions: []AssertionField{{Path: "$.a", Op: "equals", Value: `"ok"`}, {}},
	Errors:     map[string]string{"name": "Enter a name.", "proxy_url": "bad", "secret_headers": "bad", "form": "x"},
}

// The monitor form: an error summary that links to the fields in page
// order, Advanced opened by its own errors, secrets never prefilled.
func TestMonitorFormPage(t *testing.T) {
	page := NewPage("t", "", "")
	html := renderString(t, MonitorFormPage(page, testMonitorForm))
	for _, want := range []string{
		`<h2 id="summary-title">Fix 3 problems to save</h2>`,
		`<a href="#name">Name</a>: Enter a name.`,
		`aria-describedby="basic_user-keep"`,
		`id="sh_value.X-Api-Key"`,
		`<option value="m2" selected>DB</option>`,
		`API &lt;b&gt;`,
		`action="/monitors/m1"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("form lacks %q", want)
		}
	}
	if strings.Index(html, `href="#name"`) > strings.Index(html, `href="#proxy_url"`) {
		t.Error("summary is not in page order")
	}
	for _, in := range inputRe.FindAllString(html, -1) {
		if strings.Contains(in, `type="password"`) && strings.Contains(in, "value=") {
			t.Errorf("a secret input has a value: %s", in)
		}
	}
	// A section opens for its own errors only: Advanced (proxy_url) and
	// Request (secret_headers); the others stay closed.
	for key, open := range map[string]bool{"advanced": true, "request": true, "checking": false, "assertions": false, "alerts": false} {
		if tag := sectionTag(html, key); tag == "" || strings.Contains(tag, " open") != open {
			t.Errorf("section %s: %q, want open=%v", key, tag, open)
		}
	}
	clean := renderString(t, MonitorFormPage(page, MonitorForm{Type: "http"}))
	for _, key := range []string{"checking", "assertions", "request", "alerts", "advanced"} {
		if strings.Contains(sectionTag(clean, key), " open") {
			t.Errorf("clean form: section %s is open", key)
		}
	}
	if strings.Contains(clean, "summary-title") || strings.Contains(clean, `class="form-row" id="section-checking" open`) {
		t.Error("a clean form shows errors or an open section")
	}
	// Name and URL come first and stay outside the sections.
	if strings.Index(clean, `id="url"`) > strings.Index(clean, `id="name"`) || strings.Index(clean, `id="name"`) > strings.Index(clean, `class="form-row"`) {
		t.Error("URL, then Name, do not come before the sections")
	}
	// Test now is a plain button after the submit button, hidden until the
	// script shows it.
	if i, j := strings.Index(clean, `type="submit"`), strings.Index(clean, `id="test-now"`); i < 0 || j < i {
		t.Error("Test now is missing or before the submit button")
	}
}

// sectionTag is the opening <details> tag of a form section, "" if absent.
func sectionTag(html, key string) string {
	i := strings.Index(html, `id="section-`+key+`"`)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(html[:i], "<details")
	end := strings.Index(html[i:], ">")
	return html[start : i+end+1]
}

// Section summaries say what a closed section holds, in words, and never
// show a secret.
func TestSectionSummary(t *testing.T) {
	f := MonitorForm{Type: "http", Interval: "30", Timeout: "5", FailureThreshold: "2", RetryDelay: "5", SuccessThreshold: "1",
		ExpectedStatus: "200-399", BodyContains: "Software for people with work to do", Method: "GET", FollowRedirects: true,
		Headers: "Accept: text/html\nX-Env: prod", Auth: "bearer", HasBearer: true, SecretHeaders: []string{"X-Api-Key"},
		Profiles: []Option{{"p1", "On-call"}}, Profile: "p1", Parents: []Option{{"m2", "DB"}},
		TLSExpiry: true, TLSDays: "30, 14, 7", MaxBodyKiB: "1024",
		Assertions: []AssertionField{{Path: "$.status", Op: "equals", Value: `"ok"`}, {}}}
	for key, want := range map[string]string{
		"checking":   "Every 30 s · timeout 5 s · down after 2 failures",
		"assertions": "Status 200–399 · body contains “Software for people…” · 1 JSON check",
		"request":    "GET · follows redirects · 2 headers · bearer token · 1 secret header",
		"alerts":     "Notifies On-call · no dependency",
		"advanced":   "Certificate warning 30, 14, 7 days",
	} {
		if got := f.SectionSummary(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	g := MonitorForm{Type: "heartbeat", FailureThreshold: "1", RetryDelay: "5", Parent: "m2", Parents: []Option{{"m2", "DB"}},
		Method: "POST", Auth: "basic", TLSDays: "7", Insecure: true, Proxy: "http://proxy:3128", IPFamily: "ipv6", MaxBodyKiB: "64"}
	for key, want := range map[string]string{
		"checking":   "Down after 1 failure · retry after 5 s",
		"alerts":     "No notifications · depends on DB",
		"request":    "POST · no redirects · basic auth",
		"advanced":   "No certificate warning · accepts invalid certificates · through a proxy · IPv6 only · body limit 64 KiB",
		"assertions": "Status 200–399 · any body",
	} {
		if got := g.SectionSummary(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	d := MonitorForm{Type: "dns", Resolver: "192.0.2.53", Expected: "192.0.2.1\n\n192.0.2.2", MatchMode: "any", SourceLabel: "backup job on db-1"}
	for key, want := range map[string]string{
		"query":  "Resolver 192.0.2.53 · expects any of 2 values",
		"source": "From backup job on db-1",
	} {
		if got := d.SectionSummary(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := (MonitorForm{Type: "dns"}).SectionSummary("query"); got != "This server's resolvers · any answer counts" {
		t.Errorf("default query = %q", got)
	}
	if got := (MonitorForm{Type: "heartbeat"}).SectionSummary("source"); got != "No source label" {
		t.Errorf("empty source = %q", got)
	}
}

// Each type shows its own target fields and none of the HTTP ones; the
// type is chosen by links on create and fixed on edit.
func TestMonitorFormTypes(t *testing.T) {
	page := NewPage("t", "", "")
	for typ, want := range map[string][]string{
		"tcp":       {`id="host"`, `id="port"`},
		"icmp":      {`id="host"`, "permission error"},
		"dns":       {`id="hostname"`, `id="query_type"`, `id="resolver"`, `id="expected"`, `id="match_mode"`},
		"heartbeat": {`id="expected_interval"`, `id="grace"`, `id="source_label"`, "shown once"},
	} {
		html := renderString(t, MonitorFormPage(page, MonitorForm{Type: typ}))
		for _, w := range append(want, `name="type" value="`+typ+`"`, `href="/monitors/new?type=`+typ+`" aria-current="page"`) {
			if !strings.Contains(html, w) {
				t.Errorf("%s form lacks %q", typ, w)
			}
		}
		for _, absent := range []string{`id="url"`, `id="expected_status"`, `id="section-advanced"`, `id="section-assertions"`, `id="section-request"`} {
			if strings.Contains(html, absent) {
				t.Errorf("%s form has %q", typ, absent)
			}
		}
		if typ == "heartbeat" && (strings.Contains(html, `id="interval"`) || strings.Contains(html, `id="timeout"`)) {
			t.Error("heartbeat form has an interval or timeout")
		}
		if hasTest := strings.Contains(html, `id="test-now"`); hasTest != (typ != "heartbeat") {
			t.Errorf("%s form: Test now shown = %v", typ, hasTest)
		}
	}
	html := renderString(t, MonitorFormPage(page, MonitorForm{ID: "m1", Type: "dns", QueryType: "MX", MatchMode: "any"}))
	for _, w := range []string{`<p class="type-fixed">DNS</p>`, `<option value="MX" selected>`, `<option value="any" selected>`} {
		if !strings.Contains(html, w) {
			t.Errorf("dns edit form lacks %q", w)
		}
	}
	if strings.Contains(html, `name="type"`) || strings.Contains(html, "type-cards") {
		t.Error("the edit form offers a type change")
	}
}

var (
	invalidRe   = regexp.MustCompile(`<[a-z]+\b[^>]*aria-invalid="true"[^>]*>`)
	describedRe = regexp.MustCompile(`aria-describedby="([^"]+)"`)
)

// An invalid field points at the element that says why (docs/22 "error
// association"): every id its aria-describedby names exists on the page.
func TestInvalidFieldsPointAtTheirErrors(t *testing.T) {
	page := NewPage("t", "", "")
	signedIn := page
	signedIn.Admin = true
	html := renderString(t, StatusPageFormPage(signedIn, testStatusPageForm))
	ids := map[string]bool{}
	for _, m := range idRe.FindAllStringSubmatch(html, -1) {
		ids[m[1]] = true
	}
	tags := invalidRe.FindAllString(html, -1)
	if len(tags) == 0 {
		t.Fatal("no invalid field rendered")
	}
	for _, tag := range tags {
		m := describedRe.FindStringSubmatch(tag)
		if m == nil {
			t.Errorf("invalid field has no aria-describedby: %s", tag)
			continue
		}
		for _, id := range strings.Fields(m[1]) {
			if !ids[id] {
				t.Errorf("aria-describedby names %q, which is not on the page: %s", id, tag)
			}
		}
	}
}
