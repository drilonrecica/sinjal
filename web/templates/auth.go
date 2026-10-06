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
	Login string
	Next  string
	Error string
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
