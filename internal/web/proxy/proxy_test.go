package proxy

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func prefixes(t *testing.T, s ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, v := range s {
		out = append(out, netip.MustParsePrefix(v))
	}
	return out
}

func TestResolve(t *testing.T) {
	trusted := prefixes(t, "10.0.0.0/8", "fd00::/8", "::ffff:192.168.1.0/120")
	tests := []struct {
		name       string
		trusted    []netip.Prefix
		remote     string
		tls        bool
		host       string
		hdr        map[string][]string
		wantIP     string
		wantScheme string
		wantHost   string
	}{
		{
			name: "no proxies configured ignores all forwarded headers", remote: "10.0.0.1:5000", host: "a.example",
			hdr:    map[string][]string{"X-Forwarded-For": {"203.0.113.9"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"evil.example"}},
			wantIP: "10.0.0.1", wantScheme: "http", wantHost: "a.example",
		},
		{
			name: "untrusted peer spoofing is ignored", trusted: trusted, remote: "198.51.100.7:5000", host: "a.example",
			hdr:    map[string][]string{"X-Forwarded-For": {"203.0.113.9"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"evil.example"}},
			wantIP: "198.51.100.7", wantScheme: "http", wantHost: "a.example",
		},
		{
			name: "direct TLS without proxy", remote: "198.51.100.7:5000", tls: true, host: "A.Example",
			wantIP: "198.51.100.7", wantScheme: "https", wantHost: "a.example",
		},
		{
			name: "trusted proxy", trusted: trusted, remote: "10.1.2.3:5000", host: "sinjal:8080",
			hdr:    map[string][]string{"X-Forwarded-For": {"203.0.113.9"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"Status.Example.com"}},
			wantIP: "203.0.113.9", wantScheme: "https", wantHost: "status.example.com",
		},
		{
			name: "trusted proxy without headers", trusted: trusted, remote: "10.1.2.3:5000", host: "sinjal:8080",
			wantIP: "10.1.2.3", wantScheme: "http", wantHost: "sinjal:8080",
		},
		{
			name: "client-supplied left entries are not trusted", trusted: trusted, remote: "10.1.2.3:5000",
			hdr:    map[string][]string{"X-Forwarded-For": {"1.1.1.1, 203.0.113.9"}},
			wantIP: "203.0.113.9", wantScheme: "http",
		},
		{
			name: "chain of trusted proxies is skipped", trusted: trusted, remote: "10.1.2.3:5000",
			hdr:    map[string][]string{"X-Forwarded-For": {"1.1.1.1, 203.0.113.9, 10.9.9.9", "10.8.8.8"}},
			wantIP: "203.0.113.9", wantScheme: "http",
		},
		{
			name: "all hops trusted yields leftmost", trusted: trusted, remote: "10.1.2.3:5000",
			hdr:    map[string][]string{"X-Forwarded-For": {"10.5.5.5, 10.6.6.6"}},
			wantIP: "10.5.5.5", wantScheme: "http",
		},
		{
			name: "malformed hop stops the walk", trusted: trusted, remote: "10.1.2.3:5000",
			hdr:    map[string][]string{"X-Forwarded-For": {"203.0.113.9, garbage, 10.6.6.6"}},
			wantIP: "10.6.6.6", wantScheme: "http",
		},
		{
			name: "malformed only hop keeps peer", trusted: trusted, remote: "10.1.2.3:5000",
			hdr:    map[string][]string{"X-Forwarded-For": {"unknown"}},
			wantIP: "10.1.2.3", wantScheme: "http",
		},
		{
			name: "xff with port and bracketed ipv6", trusted: trusted, remote: "[fd00::1]:5000",
			hdr:    map[string][]string{"X-Forwarded-For": {"[2001:db8::5]:1234"}},
			wantIP: "2001:db8::5", wantScheme: "http",
		},
		{
			name: "4in6 peer matches v4 prefix", trusted: trusted, remote: "[::ffff:10.0.0.9]:5000",
			hdr:    map[string][]string{"X-Forwarded-For": {"::ffff:203.0.113.9"}},
			wantIP: "203.0.113.9", wantScheme: "http",
		},
		{
			name: "4in6 configured prefix matches v4 peer", trusted: trusted, remote: "192.168.1.20:5000",
			hdr:    map[string][]string{"X-Forwarded-For": {"203.0.113.9"}},
			wantIP: "203.0.113.9", wantScheme: "http",
		},
		{
			name: "rightmost proto and host win; invalid values fall back", trusted: trusted, remote: "10.1.2.3:5000", host: "sinjal",
			hdr:    map[string][]string{"X-Forwarded-Proto": {"https, ftp"}, "X-Forwarded-Host": {"good.example", "bad.example/path"}},
			wantIP: "10.1.2.3", wantScheme: "http", wantHost: "sinjal",
		},
		{
			name: "rightmost proto across lines", trusted: trusted, remote: "10.1.2.3:5000", host: "sinjal",
			hdr:    map[string][]string{"X-Forwarded-Proto": {"http", "HTTPS"}, "X-Forwarded-Host": {"x.example, y.example:8443"}},
			wantIP: "10.1.2.3", wantScheme: "https", wantHost: "y.example:8443",
		},
		{
			name: "host with userinfo rejected", trusted: trusted, remote: "10.1.2.3:5000", host: "sinjal",
			hdr:    map[string][]string{"X-Forwarded-Host": {"user@evil.example"}},
			wantIP: "10.1.2.3", wantScheme: "http", wantHost: "sinjal",
		},
		{
			name: "Forwarded header is never read", trusted: trusted, remote: "10.1.2.3:5000",
			hdr:    map[string][]string{"Forwarded": {"for=203.0.113.9;proto=https"}},
			wantIP: "10.1.2.3", wantScheme: "http",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remote
			r.Host = tt.host
			if tt.tls {
				r.TLS = &tls.ConnectionState{}
			}
			for k, vs := range tt.hdr {
				for _, v := range vs {
					r.Header.Add(k, v)
				}
			}
			got := New(tt.trusted).Resolve(r)
			if got.ClientIP.String() != tt.wantIP || got.Scheme != tt.wantScheme || got.Host != tt.wantHost {
				t.Errorf("Resolve = {%s %s %q}, want {%s %s %q}",
					got.ClientIP, got.Scheme, got.Host, tt.wantIP, tt.wantScheme, tt.wantHost)
			}
		})
	}
}

func TestMiddlewareAndHelpers(t *testing.T) {
	var ip, host string
	var https bool
	h := Middleware(New(prefixes(t, "127.0.0.1/32")))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, https, host = ClientIP(r).String(), IsHTTPS(r), Host(r)
	}))
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "s.example")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if ip != "203.0.113.9" || !https || host != "s.example" {
		t.Errorf("helpers = %s %v %s", ip, https, host)
	}
}

// Without the middleware, helpers must never trust forwarded headers.
func TestHelpersWithoutMiddlewareTrustNothing(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	r.Header.Set("X-Forwarded-Proto", "https")
	if ClientIP(r).String() != "127.0.0.1" || IsHTTPS(r) {
		t.Errorf("forwarded headers trusted without middleware")
	}
}
