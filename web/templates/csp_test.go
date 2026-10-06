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
		"settingsOff": SettingsAuth(page, SettingsAuthView{PasskeysUnavailable: "SINJAL_BASE_URL is not set."}),
		"totpSetup":   TOTPSetup(page, TOTPSetupView{Secret: "AAAA BBBB", URI: "otpauth://totp/x", QR: "data:image/png;base64,AAAA", Pending: "p", Error: "e"}),
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
