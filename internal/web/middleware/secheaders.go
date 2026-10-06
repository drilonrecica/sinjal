package middleware

import "net/http"

// ContentSecurityPolicy allows only Sinjal's own bundled assets: no inline
// script or style, no eval, no CDNs, no framing, forms post only to Sinjal.
// The theme is rendered on the server (data-theme), so no inline script
// needs a hash. img-src allows data: for small generated images (QR codes,
// M1-13).
const ContentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; manifest-src 'self'; " +
	"form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// PermissionsPolicy turns off powerful features Sinjal never uses. WebAuthn
// (publickey-credentials-*) keeps its default of self for passkeys.
const PermissionsPolicy = "camera=(), microphone=(), geolocation=(), payment=(), usb=(), " +
	"serial=(), midi=(), display-capture=(), browsing-topics=()"

// SecurityHeaders sets the response security headers (docs/13 "Security
// headers") before the handler runs, so every response carries them:
// pages, static files, errors, 404s and recovered panics. A handler may
// override one, as /setup does with Referrer-Policy: no-referrer.
//
// HSTS is not sent: TLS terminates at the operator's reverse proxy, which
// owns that decision for its domain.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", PermissionsPolicy)
		next.ServeHTTP(w, r)
	})
}
