// Package config loads Sinjal's runtime configuration from the environment.
// Monitors and other user-managed settings live in SQLite, not here.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	envPrefix  = "SINJAL_"
	maxWorkers = 256
)

// Config is the validated runtime configuration.
type Config struct {
	DataDir        string
	Listen         string
	BaseURL        string // empty when unset; no trailing slash
	TrustedProxies []netip.Prefix
	LogFormat      string // "text" or "json"
	LogLevel       string // "debug", "info", "warn" or "error"
	Timezone       *time.Location
	Workers        int // always >= 1 after Load
}

var knownVars = map[string]bool{
	"SINJAL_DATA_DIR":        true,
	"SINJAL_LISTEN":          true,
	"SINJAL_BASE_URL":        true,
	"SINJAL_TRUSTED_PROXIES": true,
	"SINJAL_LOG_FORMAT":      true,
	"SINJAL_LOG_LEVEL":       true,
	"SINJAL_TIMEZONE":        true,
	"SINJAL_WORKERS":         true,
}

// Load parses and validates the configuration. getenv looks up one variable
// and environ lists all of them as "KEY=value" (os.Getenv and os.Environ in
// production). Every problem is reported, not just the first.
func Load(getenv func(string) string, environ func() []string) (Config, error) {
	var errs []error
	fail := func(name, format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", name, fmt.Sprintf(format, a...)))
	}
	get := func(name, def string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return def
	}

	for _, kv := range environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, envPrefix) && !knownVars[name] {
			errs = append(errs, fmt.Errorf("%s: unknown variable (known: %s)", name, knownList()))
		}
	}

	var c Config

	dir := get("SINJAL_DATA_DIR", "/data")
	abs, err := filepath.Abs(dir)
	if err != nil {
		fail("SINJAL_DATA_DIR", "%v", err)
	}
	c.DataDir = abs

	c.Listen = get("SINJAL_LISTEN", ":8080")
	if err := validateListen(c.Listen); err != nil {
		fail("SINJAL_LISTEN", "%v", err)
	}

	if raw := get("SINJAL_BASE_URL", ""); raw != "" {
		base, err := parseBaseURL(raw)
		if err != nil {
			fail("SINJAL_BASE_URL", "%v", err)
		}
		c.BaseURL = base
	}

	if raw := get("SINJAL_TRUSTED_PROXIES", ""); raw != "" {
		prefixes, err := parseProxies(raw)
		if err != nil {
			fail("SINJAL_TRUSTED_PROXIES", "%v", err)
		}
		c.TrustedProxies = prefixes
	}

	c.LogFormat = strings.ToLower(get("SINJAL_LOG_FORMAT", "text"))
	if c.LogFormat != "text" && c.LogFormat != "json" {
		fail("SINJAL_LOG_FORMAT", "must be text or json, got %q", c.LogFormat)
	}

	c.LogLevel = strings.ToLower(get("SINJAL_LOG_LEVEL", "info"))
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		fail("SINJAL_LOG_LEVEL", "must be debug, info, warn or error, got %q", c.LogLevel)
	}

	tz := get("SINJAL_TIMEZONE", "UTC")
	if c.Timezone, err = time.LoadLocation(tz); err != nil {
		fail("SINJAL_TIMEZONE", "unknown time zone %q", tz)
	}

	c.Workers = defaultWorkers()
	if raw := get("SINJAL_WORKERS", ""); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxWorkers {
			fail("SINJAL_WORKERS", "must be an integer from 1 to %d, got %q", maxWorkers, raw)
		} else {
			c.Workers = n
		}
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return c, nil
}

// defaultWorkers is min(32, max(8, NumCPU*4)) (docs/07_SCHEDULER.md).
func defaultWorkers() int {
	return min(32, max(8, runtime.NumCPU()*4))
}

func knownList() string {
	names := make([]string, 0, len(knownVars))
	for n := range knownVars {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func validateListen(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("must be host:port or :port, got %q", addr)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port must be 1-65535, got %q", port)
	}
	return nil
}

func parseBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q", raw)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("must be an absolute http or https URL, got %q", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("must not contain credentials, a query or a fragment, got %q", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// parseProxies accepts comma-separated CIDRs and bare IPs.
func parseProxies(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if p, err := netip.ParsePrefix(item); err == nil {
			out = append(out, p)
			continue
		}
		addr, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("%q is not a CIDR or IP address", item)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}
