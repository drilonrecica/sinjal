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
		"setup":     renderString(t, Setup(page, SetupForm{Token: "t"}, 12)),
		"login":     renderString(t, Login(page, LoginForm{})),
		"loginTOTP": renderString(t, LoginTOTP(page, LoginTOTPForm{Challenge: "c"})),
		"reauth":    renderString(t, Reauth(page, ReauthForm{Login: "admin", TOTP: true})),
		"settings":  renderString(t, SettingsAuth(signedIn, SettingsAuthView{MinPassword: 12})),
		"totpSetup": renderString(t, TOTPSetup(signedIn, TOTPSetupView{Pending: "p"})),
		"account":   renderString(t, AccountPassword(signedIn, PasswordForm{}, 12)),
		"monitor":   renderString(t, MonitorFormPage(signedIn, testMonitorForm)),
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
		`<details class="form-section form-advanced" open>`,
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
	if html := renderString(t, MonitorFormPage(page, MonitorForm{Type: "http"})); strings.Contains(html, "summary-title") || strings.Contains(html, "form-advanced\" open") {
		t.Error("a clean form shows errors or an open Advanced section")
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
		for _, absent := range []string{`id="url"`, `id="expected_status"`, "form-advanced"} {
			if strings.Contains(html, absent) {
				t.Errorf("%s form has %q", typ, absent)
			}
		}
		if typ == "heartbeat" && (strings.Contains(html, `id="interval"`) || strings.Contains(html, `id="timeout"`)) {
			t.Error("heartbeat form has an interval or timeout")
		}
	}
	html := renderString(t, MonitorFormPage(page, MonitorForm{ID: "m1", Type: "dns", QueryType: "MX", MatchMode: "any"}))
	for _, w := range []string{`<p class="type-fixed">DNS</p>`, `<option value="MX" selected>`, `<option value="any" selected>`} {
		if !strings.Contains(html, w) {
			t.Errorf("dns edit form lacks %q", w)
		}
	}
	if strings.Contains(html, `name="type"`) || strings.Contains(html, "type-switch") {
		t.Error("the edit form offers a type change")
	}
}
