// Package statuspage holds the rules of public status pages that need no
// database: slugs, mapped hostnames, themes and the accent, and the
// unlisted-page token (docs/12_STATUS_PAGES.md, docs/38_CONFIG_VALIDATION.md
// "Status page"). Storage is internal/store; the admin forms are
// internal/web.
package statuspage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Visibility modes (docs/12 "Visibility modes").
const (
	Public        = "public"
	Authenticated = "authenticated"
	Password      = "password"
	Unlisted      = "unlisted"
)

// Visibilities lists the modes in display order.
var Visibilities = []string{Public, Authenticated, Password, Unlisted}

// Themes are the built-in themes a page may use (docs/05_THEMES.md).
var Themes = []string{"carbon", "paper", "midnight", "terminal"}

// DefaultTheme is the theme of a new page.
const DefaultTheme = "paper"

// Bounds of the page fields.
const (
	MaxTitleLen         = 100
	MaxDescriptionLen   = 500
	MaxNameLen          = 100
	MaxGroups           = 20
	MaxHosts            = 10
	MinPasswordLen      = 8
	MaxPasswordLen      = 128
	MinIncidentDays     = 1
	MaxIncidentDays     = 365
	DefaultIncidentDays = 30
)

var slugRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

// NormalizeSlug trims and lowercases a slug and reports whether it is a
// valid URL segment: letters, digits and inner hyphens, at most 64.
func NormalizeSlug(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	return s, slugRe.MatchString(s)
}

var labelRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// NormalizeHost trims, lowercases and drops one trailing dot from a
// hostname, and reports whether it is a plain DNS name or an IP address:
// no scheme, port, path or user. base is the host of the instance's own
// base URL (empty when unset): a mapping must never take it over, since
// the admin UI lives there.
func NormalizeHost(s, base string) (string, error) {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if h == "" {
		return "", errors.New("empty")
	}
	if len(h) > 253 {
		return "", fmt.Errorf("%q is longer than 253 characters", h[:20]+"…")
	}
	if net.ParseIP(h) == nil {
		for _, label := range strings.Split(h, ".") {
			if !labelRe.MatchString(label) {
				return "", fmt.Errorf("%q is not a host name: use only letters, digits, hyphens and dots, without a scheme, port or path", s)
			}
		}
	}
	if base != "" && h == strings.ToLower(base) {
		return "", fmt.Errorf("%q is this instance's own address, which serves the admin UI", h)
	}
	return h, nil
}

// tokenBytes is 128 bits (docs/12, decision P0-15).
const tokenBytes = 16

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewToken returns an unlisted-page token — 128 bits from crypto/rand,
// base32 in lowercase without padding — and the hash that is stored. The
// token itself is shown once and never stored.
func NewToken() (token string, hash []byte, err error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	h := sha256.Sum256(raw)
	return strings.ToLower(tokenEncoding.EncodeToString(raw)), h[:], nil
}

// HashToken returns the stored hash of a presented token, or false when it
// cannot be a token Sinjal issued.
func HashToken(token string) ([]byte, bool) {
	raw, err := tokenEncoding.DecodeString(strings.ToUpper(token))
	if err != nil || len(raw) != tokenBytes || strings.ToLower(token) != token {
		return nil, false
	}
	h := sha256.Sum256(raw)
	return h[:], true
}
