package templates

import (
	"slices"
	"strings"

	"github.com/a-h/templ"
)

// SetupForm is the state of the initial setup form. Passwords are never
// echoed back. Errors maps a field ("login", "password", "confirm") to its
// message; "form" holds an error not tied to one field.
type SetupForm struct {
	Token  string
	Login  string
	Errors map[string]string
}

// LoginForm is the state of the sign-in form. Next is the same-origin path
// to return to; Error is the single generic failure message.
type LoginForm struct {
	Login   string
	Next    string
	Error   string
	Passkey bool // offer "Sign in with a passkey"
}

// LoginTOTPForm is the second sign-in step for accounts with TOTP.
// Challenge is the opaque token proving the password was already checked.
type LoginTOTPForm struct {
	Challenge string
	Next      string
	Error     string
}

// ReauthForm is the state of the re-authentication form. Login is shown
// (and offered to password managers); it is not editable. TOTP adds the
// code field for accounts that have it enabled.
type ReauthForm struct {
	Login   string
	Next    string
	Error   string
	TOTP    bool
	Passkey bool // the user has a passkey to confirm with instead
}

// SettingsAuthView is Settings → Authentication for the signed-in admin.
type SettingsAuthView struct {
	TOTPEnabled         bool
	Passkeys            []PasskeyView
	PasskeysUnavailable string // why passkeys cannot be added; "" when they can
	Viewers             []ViewerView
	ViewerForm          ViewerForm
	MinPassword         int
}

// ViewerView is one row of the viewer list. Added is preformatted.
type ViewerView struct {
	ID       string
	Login    string
	Disabled bool
	Added    string
}

// ViewerForm is the state of the create-viewer form; passwords are never
// echoed back. Errors maps "login", "password" or "confirm" to a message.
type ViewerForm struct {
	Login  string
	Errors map[string]string
}

// PasswordForm is the change-password form; Changed shows the success
// message after the redirect.
type PasswordForm struct {
	Errors    map[string]string
	Changed   bool
	SignedOut bool // other sessions were just signed out
}

// PasskeyView is one row of the passkey list. Dates are preformatted.
type PasskeyView struct {
	ID       string
	Label    string
	Added    string
	LastUsed string
}

// passkeyScripts is the script list of a page that may show a passkey
// button.
func passkeyScripts(on bool) []string {
	if on {
		return []string{"js/passkey.js"}
	}
	return nil
}

// TOTPSetupView is the TOTP enrolment page. It shows a new secret, so it is
// only rendered after recent re-authentication. QR is a PNG data URI.
type TOTPSetupView struct {
	Secret  string // base32, grouped for reading
	URI     string
	QR      string
	Pending string
	Error   string
}

// fieldAttrs returns the accessibility attributes of an input: hint ids
// plus the error message id and aria-invalid when the field has an error.
// Empty hint ids are skipped.
func fieldAttrs(name string, errs map[string]string, hints ...string) templ.Attributes {
	attrs := templ.Attributes{}
	ids := slices.DeleteFunc(slices.Clone(hints), func(id string) bool { return id == "" })
	if _, bad := errs[name]; bad {
		attrs["aria-invalid"] = "true"
		ids = append(ids, name+"-error")
	}
	if len(ids) > 0 {
		attrs["aria-describedby"] = strings.Join(ids, " ")
	}
	return attrs
}

// SystemView is Settings → System. Rows are preformatted; OlderPath is the
// link to the next page of the audit log, "" on the last page.
type SystemView struct {
	Audit     []AuditRow
	OlderPath string
}

// AuditRow is one audit event. When is UTC.
type AuditRow struct {
	When    string
	Actor   string // "" when nobody was signed in
	Event   string
	Object  string
	Details string
}

// settingsPage is one entry of the settings navigation. Admin entries are
// hidden from viewers, who can reach only their own account page.
type settingsPage struct {
	Key   string
	Label string
	Path  string
	Admin bool
}

// settingsPages lists the settings pages that exist, in display order. The
// others from docs/03_INFORMATION_ARCHITECTURE.md join as they are built.
var settingsPages = []settingsPage{
	{"account", "Your account", "/account/password", false},
	{"authentication", "Authentication", "/settings/authentication", true},
	{"system", "System", "/settings/system", true},
}

// formErrorAttrs ties an input to the form-level alert (id "form-error")
// when failed is set: the one generic message is read with the field and
// the field is marked invalid.
func formErrorAttrs(failed bool) templ.Attributes {
	if !failed {
		return nil
	}
	return templ.Attributes{"aria-invalid": "true", "aria-describedby": "form-error"}
}
