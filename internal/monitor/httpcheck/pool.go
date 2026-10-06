// Package httpcheck executes HTTP(S) monitor checks (docs/06). Transports
// are pooled by connection-relevant settings so checks reuse connections.
package httpcheck

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Pool limits. Transports idle for evictAfter are dropped; the sweep runs
// lazily, at most every sweepEvery, when a client is requested.
const (
	MaxRedirects        = 10
	maxIdleConns        = 64
	maxIdleConnsPerHost = 2
	idleConnTimeout     = 90 * time.Second
	evictAfter          = 10 * time.Minute
	sweepEvery          = time.Minute
)

// ErrTooManyRedirects is returned (wrapped in a *url.Error) when a check
// follows more than MaxRedirects redirects.
var ErrTooManyRedirects = errors.New("stopped after 10 redirects")

// transportKey holds what changes how connections are made. Everything
// else (URL, headers, redirect policy) varies per request.
type transportKey struct {
	insecure bool
	proxy    string // validated proxy URL, "" for a direct connection
	ipFamily string // "", "ipv4" or "ipv6"
}

type entry struct {
	t        *http.Transport
	lastUsed time.Time
}

// Pool shares HTTP transports between checks. It is safe for concurrent use.
type Pool struct {
	userAgent string
	now       func() time.Time

	mu        sync.Mutex
	m         map[transportKey]*entry
	lastSweep time.Time
}

// NewPool returns an empty pool. userAgent is sent when a monitor sets no
// custom User-Agent, e.g. "Sinjal/1.2.3".
func NewPool(userAgent string) *Pool {
	return &Pool{userAgent: userAgent, now: time.Now, m: map[transportKey]*entry{}}
}

// client returns an http.Client for one check: a shared transport for the
// key and a redirect policy for this monitor. Clients are cheap; the
// transport holds the connections.
func (p *Pool) client(k transportKey, followRedirects bool) (*http.Client, error) {
	t, err := p.transport(k)
	if err != nil {
		return nil, err
	}
	c := &http.Client{Transport: t}
	if followRedirects {
		c.CheckRedirect = func(_ *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return ErrTooManyRedirects
			}
			return nil
		}
	} else {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c, nil
}

func (p *Pool) transport(k transportKey) (*http.Transport, error) {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Sub(p.lastSweep) >= sweepEvery {
		p.sweep(now)
	}
	if e, ok := p.m[k]; ok {
		e.lastUsed = now
		return e.t, nil
	}
	t, err := newTransport(k)
	if err != nil {
		return nil, err
	}
	p.m[k] = &entry{t: t, lastUsed: now}
	return t, nil
}

// sweep drops transports unused for evictAfter. Callers hold mu.
func (p *Pool) sweep(now time.Time) {
	p.lastSweep = now
	for k, e := range p.m {
		if now.Sub(e.lastUsed) >= evictAfter {
			e.t.CloseIdleConnections()
			delete(p.m, k)
		}
	}
}

// Close closes every idle connection and empties the pool.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.m {
		e.t.CloseIdleConnections()
		delete(p.m, k)
	}
}

// newTransport builds a transport for k. There is no environment proxy:
// a monitor connects directly unless it configures a proxy (owner decision,
// so checks measure what they say). Phase timeouts are left to the check's
// context deadline.
func newTransport(k transportKey) (*http.Transport, error) {
	dialer := &net.Dialer{KeepAlive: 30 * time.Second}
	network := map[string]string{"": "tcp", "ipv4": "tcp4", "ipv6": "tcp6"}[k.ipFamily]
	if network == "" {
		return nil, errors.New("unknown IP family " + k.ipFamily)
	}
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: k.insecure},
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        maxIdleConns,
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
		IdleConnTimeout:     idleConnTimeout,
	}
	if k.proxy != "" {
		u, err := url.Parse(k.proxy)
		if err != nil {
			return nil, err
		}
		t.Proxy = http.ProxyURL(u)
	}
	return t, nil
}
