// Package dnscheck is the DNS monitor check (docs/06, decision P0-13): one
// query of a given type for a hostname, sent to the configured resolver or
// to the system's nameservers, with optional expected values in the answer.
//
// It speaks DNS itself (golang.org/x/net/dns/dnsmessage) rather than
// through net.Resolver, which reports NXDOMAIN and an empty answer as the
// same error, answers from /etc/hosts and appends search domains.
package dnscheck

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/drilonrecica/sinjal/internal/monitor/tcpcheck"
)

// Match modes for expected values.
const (
	MatchAll = "all" // every expected value must be in the answer (default)
	MatchAny = "any" // at least one must be
)

// MaxExpected caps the expected values of one monitor.
const MaxExpected = 32

const maxTXT = 1024 // longest expected TXT value

// queryTypes are the supported query types (docs/06).
var queryTypes = map[string]dnsmessage.Type{
	"A":     dnsmessage.TypeA,
	"AAAA":  dnsmessage.TypeAAAA,
	"CNAME": dnsmessage.TypeCNAME,
	"MX":    dnsmessage.TypeMX,
	"TXT":   dnsmessage.TypeTXT,
	"NS":    dnsmessage.TypeNS,
}

// Config is one DNS check, built from a monitor's validated settings.
type Config struct {
	Hostname  string
	QueryType string // A, AAAA, CNAME, MX, TXT or NS
	Resolver  string // "" for the system's nameservers, else IP or IP:port
	Expected  []string
	MatchMode string // MatchAll ("" too) or MatchAny
	Timeout   time.Duration
}

// Validate checks a DNS monitor's settings (docs/38): hostname, query
// type, resolver address, match mode, each expected value against the
// query type, and a positive timeout.
func (c Config) Validate() error {
	if err := tcpcheck.ValidateHost(c.Hostname); err != nil {
		return fmt.Errorf("hostname: %w", err)
	}
	if _, err := netip.ParseAddr(c.Hostname); err == nil {
		return errors.New("hostname must be a name, not an IP address")
	}
	if _, ok := queryTypes[c.QueryType]; !ok {
		return errors.New("query type must be A, AAAA, CNAME, MX, TXT or NS")
	}
	if c.Resolver != "" {
		if _, err := ResolverAddr(c.Resolver); err != nil {
			return err
		}
	}
	if c.MatchMode != "" && c.MatchMode != MatchAll && c.MatchMode != MatchAny {
		return errors.New("match mode must be all or any")
	}
	if len(c.Expected) > MaxExpected {
		return fmt.Errorf("at most %d expected values", MaxExpected)
	}
	for _, v := range c.Expected {
		if _, err := parseExpected(c.QueryType, v); err != nil {
			return fmt.Errorf("expected value %q: %w", v, err)
		}
	}
	if c.Timeout <= 0 {
		return errors.New("timeout must be greater than zero")
	}
	return nil
}

// ResolverAddr turns a configured resolver into host:port: an IP address,
// optionally with a port (IPv6 with a port in brackets). The port is 53
// when none is given.
func ResolverAddr(s string) (string, error) {
	if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
		return net.JoinHostPort(a.String(), "53"), nil
	}
	ap, err := netip.ParseAddrPort(s)
	if err != nil || ap.Addr().Zone() != "" || ap.Port() == 0 {
		return "", errors.New("resolver must be an IP address, optionally with a port (192.0.2.1:53, [2001:db8::1]:53)")
	}
	return ap.String(), nil
}

// expected is one parsed expected value.
type expected struct {
	value string // normalized, as answers are (see normalize)
	host  string // MX without a preference: the host to look for
}

// parseExpected normalizes an expected value for its query type.
// TXT values are kept as given: they are compared exactly.
func parseExpected(qtype, v string) (expected, error) {
	if qtype != "TXT" {
		v = strings.TrimSpace(v)
	}
	if v == "" {
		return expected{}, errors.New("must not be empty")
	}
	switch qtype {
	case "A", "AAAA":
		a, err := netip.ParseAddr(v)
		if err != nil || a.Zone() != "" {
			return expected{}, errors.New("not an IP address")
		}
		if qtype == "A" && !a.Is4() {
			return expected{}, errors.New("an A record holds an IPv4 address")
		}
		if qtype == "AAAA" && !a.Is6() {
			return expected{}, errors.New("an AAAA record holds an IPv6 address")
		}
		return expected{value: a.String()}, nil
	case "CNAME", "NS":
		name, err := parseName(v)
		return expected{value: name}, err
	case "MX":
		fields := strings.Fields(v)
		switch len(fields) {
		case 1:
			name, err := parseName(fields[0])
			return expected{host: name}, err
		case 2:
			pref, err := strconv.ParseUint(fields[0], 10, 16)
			if err != nil {
				return expected{}, errors.New("MX is \"host\" or \"preference host\"")
			}
			name, err := parseName(fields[1])
			return expected{value: mxValue(uint16(pref), name)}, err
		default:
			return expected{}, errors.New("MX is \"host\" or \"preference host\"")
		}
	case "TXT":
		if len(v) > maxTXT {
			return expected{}, fmt.Errorf("longer than %d bytes", maxTXT)
		}
		return expected{value: v}, nil
	}
	return expected{}, errors.New("unknown query type")
}

func parseName(v string) (string, error) {
	if err := tcpcheck.ValidateHost(v); err != nil {
		return "", errors.New("not a valid name")
	}
	return normName(v), nil
}

// normName lowercases a name and strips the trailing dot.
func normName(v string) string { return strings.ToLower(strings.TrimSuffix(v, ".")) }

func mxValue(pref uint16, host string) string { return strconv.Itoa(int(pref)) + " " + host }
