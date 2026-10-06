package templates

import (
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
func fieldAttrs(name string, errs map[string]string, hints ...string) templ.Attributes {
	attrs := templ.Attributes{}
	ids := hints
	if _, bad := errs[name]; bad {
		attrs["aria-invalid"] = "true"
		ids = append(ids, name+"-error")
	}
	if len(ids) > 0 {
		attrs["aria-describedby"] = strings.Join(ids, " ")
	}
	return attrs
}
