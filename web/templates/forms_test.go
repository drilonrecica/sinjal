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
