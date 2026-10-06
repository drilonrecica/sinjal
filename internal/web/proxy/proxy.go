// Package proxy resolves the real client IP, scheme and host of a request
// under the trusted-proxy rules of docs/13_AUTH_SECURITY.md ("Proxy trust").
//
// X-Forwarded-For, X-Forwarded-Proto and X-Forwarded-Host are honoured only
// when the TCP peer is inside SINJAL_TRUSTED_PROXIES; otherwise they are
// ignored. The RFC 7239 Forwarded header is never read. Handlers must use
// ClientIP, IsHTTPS and Host instead of RemoteAddr, r.TLS or r.Host.
package proxy

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
)

// Info is the resolved view of a request.
type Info struct {
	ClientIP netip.Addr // invalid only if RemoteAddr is not an IP (never for TCP)
	Scheme   string     // "http" or "https"
	Host     string     // lowercased host[:port]
}

// Resolver applies the trusted-proxy rules.
type Resolver struct {
	trusted []netip.Prefix
}

// New returns a Resolver trusting the given prefixes. No prefixes trusts no
// proxy, so forwarded headers are always ignored.
func New(trusted []netip.Prefix) *Resolver {
	r := &Resolver{}
	for _, p := range trusted {
		// Peers are unmapped, so ::ffff:a.b.c.d/N must become a.b.c.d/(N-96).
		if p.Addr().Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		r.trusted = append(r.trusted, p.Masked())
	}
	return r
}

func (res *Resolver) isTrusted(a netip.Addr) bool {
	for _, p := range res.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Resolve computes the request's Info.
func (res *Resolver) Resolve(r *http.Request) Info {
	info := Info{Scheme: "http", Host: strings.ToLower(r.Host)}
	if r.TLS != nil {
		info.Scheme = "https"
	}
	peer, ok := parseAddr(r.RemoteAddr)
	if !ok {
		return info
	}
	info.ClientIP = peer
	if !res.isTrusted(peer) {
		return info
	}

	info.ClientIP = res.clientFromXFF(r.Header.Values("X-Forwarded-For"), peer)
	if p := lastValue(r.Header.Values("X-Forwarded-Proto")); p != "" {
		if p = strings.ToLower(p); p == "http" || p == "https" {
			info.Scheme = p
		}
	}
	if h := lastValue(r.Header.Values("X-Forwarded-Host")); validHost(h) {
		info.Host = strings.ToLower(h)
	}
	return info
}

// clientFromXFF walks X-Forwarded-For from the right, skipping trusted hops.
// The first untrusted address is the client. A malformed entry ends the walk
// at the last valid hop: anything left of it cannot be attributed to a
// trusted proxy. If every hop is trusted, the leftmost one is the client.
func (res *Resolver) clientFromXFF(lines []string, peer netip.Addr) netip.Addr {
	var hops []string
	for _, l := range lines {
		hops = append(hops, strings.Split(l, ",")...)
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := parseAddr(strings.TrimSpace(hops[i]))
		if !ok {
			break
		}
		client = a
		if !res.isTrusted(a) {
			break
		}
	}
	return client
}

// parseAddr accepts "ip", "ip:port" and "[ipv6]:port", dropping any IPv6
// zone and unmapping IPv4-in-IPv6.
func parseAddr(s string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	if a, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")); err == nil {
		return a.Unmap().WithZone(""), true
	}
	return netip.Addr{}, false
}

// lastValue returns the rightmost comma-separated value: the one added by
// the immediate (trusted) proxy.
func lastValue(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	l := lines[len(lines)-1]
	return strings.TrimSpace(l[strings.LastIndexByte(l, ',')+1:])
}

// validHost accepts a plain host[:port]: letters, digits, '.', '-', ':' and
// IPv6 brackets. Anything that could change a URL's meaning is rejected.
func validHost(h string) bool {
	if h == "" || len(h) > 255 {
		return false
	}
	for _, c := range h {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '-', c == ':', c == '[', c == ']':
		default:
			return false
		}
	}
	return true
}

type infoKey struct{}

// Middleware resolves every request once and stores the Info in its context.
func Middleware(res *Resolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			info := res.Resolve(r)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), infoKey{}, info)))
		})
	}
}

// FromContext returns the Info stored by Middleware.
func FromContext(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(infoKey{}).(Info)
	return info, ok
}

// fromRequest falls back to resolving with no trusted proxies, so a handler
// mounted without Middleware still never trusts forwarded headers.
func fromRequest(r *http.Request) Info {
	if info, ok := FromContext(r.Context()); ok {
		return info
	}
	return (&Resolver{}).Resolve(r)
}

// ClientIP returns the real client address.
func ClientIP(r *http.Request) netip.Addr { return fromRequest(r).ClientIP }

// IsHTTPS reports whether the client reached Sinjal over HTTPS, directly or
// through a trusted proxy.
func IsHTTPS(r *http.Request) bool { return fromRequest(r).Scheme == "https" }

// Host returns the lowercased host the client asked for.
func Host(r *http.Request) string { return fromRequest(r).Host }
