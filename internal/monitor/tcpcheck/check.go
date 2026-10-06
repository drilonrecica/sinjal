// Package tcpcheck is the TCP monitor check (docs/06): connect to host:port
// within the timeout, record how long the connect took and close at once.
// No data is sent or read.
package tcpcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Failure kinds (docs/30). Same strings as the HTTP check; stored in
// check_results.error_kind.
const (
	KindTimeout = "timeout"
	KindDNS     = "dns"
	KindConnect = "connect"
	KindUnknown = "unknown"
)

// Config is one TCP check, built from a monitor's validated settings.
type Config struct {
	Host    string
	Port    int
	Timeout time.Duration
}

// Result is the outcome of one check. Duration is the connect time for a
// successful check and the time to the failure otherwise.
type Result struct {
	Started  time.Time
	Finished time.Time
	Duration time.Duration
	Success  bool
	Kind     string // "" on success
	Message  string
}

// Check dials the target and closes the connection immediately. It never
// returns an error: every failure is a Result with a kind.
func Check(ctx context.Context, cfg Config) Result {
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	start := time.Now()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err == nil {
		// Connect time is measured before the close, which is not part of it.
		res := finish(Result{Started: start, Success: true})
		conn.Close()
		return res
	}
	kind, msg := classify(ctx, err, cfg.Timeout)
	return finish(Result{Started: start, Kind: kind, Message: msg})
}

func finish(r Result) Result {
	r.Finished = time.Now()
	r.Duration = r.Finished.Sub(r.Started)
	return r
}

// classify maps a dial error to a failure kind. A check cut off by the
// caller (shutdown) is "unknown": it is not a verdict on the target.
func classify(ctx context.Context, err error, timeout time.Duration) (string, string) {
	var (
		netErr net.Error
		dnsErr *net.DNSError
		opErr  *net.OpError
	)
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return KindUnknown, "check cancelled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return KindTimeout, fmt.Sprintf("no connection within %s", timeout)
	case errors.As(err, &dnsErr):
		return KindDNS, dnsErr.Error()
	case errors.As(err, &opErr):
		return KindConnect, err.Error()
	default:
		return KindUnknown, err.Error()
	}
}

// Validate checks a TCP monitor's target (docs/38): a hostname or IP address
// without scheme, port or brackets, a port from 1 to 65535 and a positive
// timeout. Private and loopback targets are allowed (docs/34).
func (c Config) Validate() error {
	if err := ValidateHost(c.Host); err != nil {
		return err
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if c.Timeout <= 0 {
		return errors.New("timeout must be greater than zero")
	}
	return nil
}

// ValidateHost accepts an IP address or a DNS name (labels of letters,
// digits, hyphens and underscores, up to 63 characters each, 253 in all).
// A trailing dot is allowed.
func ValidateHost(host string) error {
	if host == "" {
		return errors.New("host is required")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		if strings.Contains(host, "%") {
			return errors.New("host must not contain a zone")
		}
		return nil
	}
	name := strings.TrimSuffix(host, ".")
	if name == "" || len(name) > 253 {
		return errors.New("host is not a valid name")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("host is not a valid name")
		}
		for _, r := range label {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
			if !ok {
				return errors.New("host is not a valid name")
			}
		}
	}
	return nil
}
