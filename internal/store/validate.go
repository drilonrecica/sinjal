package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/drilonrecica/sinjal/internal/monitor"
)

// Validation bounds (docs/38). The body cap ceiling is the 1 MiB hard read
// cap of docs/06; an admin may only lower it.
const (
	MinIntervalSeconds  = 10
	MaxIntervalSeconds  = 86400
	MaxThreshold        = 100
	MinMaxBodyBytes     = 1024
	MaxMaxBodyBytes     = 1048576
	MaxURLLen           = 2048
	MaxRequestBodyBytes = 64 << 10
	MaxBodyMatchBytes   = 4096
	MaxHeaders          = 32
	MaxHeaderValueBytes = 8192
	MaxUserAgentLen     = 256
	MaxJSONAssertions   = 20
	MaxTLSWarningDays   = 10
	MaxTLSWarningDay    = 365
)

// FieldErrors is a failed validation: one message per form field. Every
// field is checked, so a form can show all problems at once.
type FieldErrors map[string]string

func (e FieldErrors) Error() string {
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + ": " + e[k]
	}
	return strings.Join(parts, "; ")
}

// add keeps the first message per field.
func (e FieldErrors) add(field, msg string) {
	if _, ok := e[field]; !ok {
		e[field] = msg
	}
}

// validate checks every rule that needs no database and normalises what it
// accepts: the method is upper-cased, the status expression and TLS warning
// days take their canonical form. id is "" for a new monitor.
func (m *HTTPMonitor) validate(id string) FieldErrors {
	errs := FieldErrors{}
	switch {
	case m.Name == "":
		errs.add("name", "Enter a name.")
	case len([]rune(m.Name)) > MaxNameLen:
		errs.add("name", fmt.Sprintf("Use at most %d characters.", MaxNameLen))
	}
	if m.IntervalSeconds < MinIntervalSeconds || m.IntervalSeconds > MaxIntervalSeconds {
		errs.add("interval_seconds", fmt.Sprintf("Use an interval from %d seconds to 24 hours.", MinIntervalSeconds))
	}
	switch {
	case m.TimeoutMS <= 0:
		errs.add("timeout_ms", "Use a timeout above zero.")
	case m.TimeoutMS >= m.IntervalSeconds*1000:
		errs.add("timeout_ms", "The timeout must be shorter than the interval.")
	}
	if m.FailureThreshold < 1 || m.FailureThreshold > MaxThreshold {
		errs.add("failure_threshold", fmt.Sprintf("Use a number from 1 to %d.", MaxThreshold))
	}
	if m.SuccessThreshold < 1 || m.SuccessThreshold > MaxThreshold {
		errs.add("success_threshold", fmt.Sprintf("Use a number from 1 to %d.", MaxThreshold))
	}
	if m.RetryDelayMS < 0 {
		errs.add("retry_delay_ms", "The retry delay cannot be negative.")
	}
	if id != "" && m.ParentMonitorID == id {
		errs.add("parent_monitor_id", "A monitor cannot depend on itself.")
	}
	for _, t := range m.Tags {
		if _, err := NormalizeTag(t); err != nil {
			errs.add("tags", err.(*InputError).Message)
		}
	}
	m.Config.validate(errs)
	return errs
}

func (c *HTTPConfig) validate(errs FieldErrors) {
	if msg := checkURL(c.URL, []string{"http", "https"}, MaxURLLen, "Use the authentication setting for credentials, so they are stored encrypted."); msg != "" {
		errs.add("url", msg)
	}

	c.Method = strings.ToUpper(c.Method)
	switch c.Method {
	case "GET", "HEAD", "POST":
	default:
		errs.add("method", "Use GET, HEAD or POST.")
	}
	if c.RequestBody != "" && c.Method != "POST" {
		errs.add("request_body", "Only POST requests send a body.")
	} else if len(c.RequestBody) > MaxRequestBodyBytes {
		errs.add("request_body", "Use a request body of at most 64 KiB.")
	}

	if s, err := monitor.ParseStatus(c.ExpectedStatus); err != nil {
		errs.add("expected_status", "Use codes such as 200, 200-299 or 200,204: "+err.Error()+".")
	} else {
		c.ExpectedStatus = s.String()
	}

	if c.MaxBodyBytes < MinMaxBodyBytes || c.MaxBodyBytes > MaxMaxBodyBytes {
		errs.add("max_body_bytes", "Use a body limit from 1 KiB to 1 MiB.")
	}
	if len(c.BodyContains) > MaxBodyMatchBytes {
		errs.add("body_contains", "Use at most 4096 bytes.")
	}
	if len(c.BodyNotContains) > MaxBodyMatchBytes {
		errs.add("body_not_contains", "Use at most 4096 bytes.")
	}

	if msg := checkJSONAssertions(c.JSONAssertions); msg != "" {
		errs.add("json_assertions", msg)
	}
	if msg := checkHeaders(c.Headers); msg != "" {
		errs.add("headers", msg)
	}

	if len(c.CustomUserAgent) > MaxUserAgentLen || !monitor.ValidHeaderValue(c.CustomUserAgent) {
		errs.add("custom_user_agent", "Use at most 256 characters, without control characters.")
	}

	if days, msg := normalizeTLSDays(c.TLSWarningDays); msg != "" {
		errs.add("tls_warning_days", msg)
	} else {
		c.TLSWarningDays = days
	}

	if c.ProxyURL != "" {
		if msg := checkURL(c.ProxyURL, []string{"http", "https", "socks5"}, MaxURLLen, "Proxy credentials are not supported."); msg != "" {
			errs.add("proxy_url", msg)
		}
	}
	switch c.IPFamily {
	case "", "ipv4", "ipv6":
	default:
		errs.add("ip_family", "Use automatic, IPv4 or IPv6.")
	}
}

// checkURL returns a message when raw is not an absolute URL with one of
// schemes and a host, or carries user info.
func checkURL(raw string, schemes []string, maxLen int, userinfoMsg string) string {
	if raw == "" {
		return "Enter a URL."
	}
	if len(raw) > maxLen {
		return fmt.Sprintf("Use at most %d characters.", maxLen)
	}
	u, err := url.Parse(raw)
	if err != nil || !slices.Contains(schemes, u.Scheme) || u.Hostname() == "" {
		return "Enter a full URL starting with " + strings.Join(schemes, "://, ") + "://."
	}
	if u.User != nil {
		return "Remove the user name and password from the URL. " + userinfoMsg
	}
	return ""
}

func checkHeaders(text string) string {
	hs, err := monitor.ParseHeaders(text)
	if err != nil {
		return "Headers are malformed."
	}
	if len(hs) > MaxHeaders {
		return fmt.Sprintf("Use at most %d headers.", MaxHeaders)
	}
	seen := map[string]bool{}
	for _, h := range hs {
		lower := strings.ToLower(h.Name)
		switch {
		case !monitor.ValidHeaderName(h.Name):
			return fmt.Sprintf("%q is not a valid header name.", h.Name)
		case lower == "authorization" || lower == "proxy-authorization":
			return "Use the authentication setting instead of an " + h.Name + " header, so the value is stored encrypted."
		case lower == "host":
			return "The Host header comes from the URL."
		case seen[lower]:
			return fmt.Sprintf("The header %s is set twice.", h.Name)
		case len(h.Value) > MaxHeaderValueBytes || !monitor.ValidHeaderValue(h.Value):
			return fmt.Sprintf("The value of %s is too long or contains control characters.", h.Name)
		}
		seen[lower] = true
	}
	return ""
}

func checkJSONAssertions(text string) string {
	as, err := monitor.ParseJSONAssertions(text)
	if err != nil {
		return "JSON assertions are malformed."
	}
	if len(as) > MaxJSONAssertions {
		return fmt.Sprintf("Use at most %d JSON assertions.", MaxJSONAssertions)
	}
	for i, a := range as {
		if err := monitor.ValidateJSONAssertion(a); err != nil {
			return fmt.Sprintf("Assertion %d: %s.", i+1, err)
		}
	}
	return ""
}

// normalizeTLSDays parses a JSON list of warning days and returns it
// deduplicated and sorted from largest to smallest.
func normalizeTLSDays(text string) (string, string) {
	var days []int
	if err := json.Unmarshal([]byte(text), &days); err != nil {
		return "", "Use a list of whole days, such as 30, 14, 7."
	}
	slices.Sort(days)
	days = slices.Compact(days)
	slices.Reverse(days)
	if len(days) > MaxTLSWarningDays {
		return "", fmt.Sprintf("Use at most %d warning thresholds.", MaxTLSWarningDays)
	}
	for _, d := range days {
		if d < 1 || d > MaxTLSWarningDay {
			return "", fmt.Sprintf("Use days from 1 to %d.", MaxTLSWarningDay)
		}
	}
	out, _ := json.Marshal(days)
	if days == nil {
		out = []byte("[]")
	}
	return string(out), ""
}

// checkRefs runs the rules that read the database inside the write
// transaction, so they hold for what is committed: the parent and the
// notification profile exist, and the parent chain does not lead back to
// the monitor (docs/38). id is "" for a new monitor, which no chain can
// reach yet. It returns errs when any rule (including the ones checked
// before the transaction) failed.
func checkRefs(ctx context.Context, x execer, id string, m *HTTPMonitor, errs FieldErrors) error {
	if m.ParentMonitorID != "" && m.ParentMonitorID != id {
		cur := m.ParentMonitorID
		// Each step moves to a distinct monitor, so the walk ends at a root,
		// at id, or (for an already corrupt chain) after maxDepth steps.
		for depth := 0; ; depth++ {
			var next sql.NullString
			err := x.QueryRowContext(ctx, `SELECT parent_monitor_id FROM monitors WHERE id = ?`, cur).Scan(&next)
			if errors.Is(err, sql.ErrNoRows) {
				if cur == m.ParentMonitorID {
					errs.add("parent_monitor_id", "The parent monitor does not exist.")
				}
				break
			}
			if err != nil {
				return err
			}
			if !next.Valid {
				break
			}
			if next.String == id || depth == maxParentDepth {
				errs.add("parent_monitor_id", "This parent would create a dependency cycle.")
				break
			}
			cur = next.String
		}
	}
	if m.NotificationProfileID != "" {
		var one int
		err := x.QueryRowContext(ctx, `SELECT 1 FROM notification_profiles WHERE id = ?`, m.NotificationProfileID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			errs.add("notification_profile_id", "The notification profile does not exist.")
		} else if err != nil {
			return err
		}
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// maxParentDepth bounds the parent walk. Real dependency trees are a few
// levels deep; a longer chain is treated as a cycle.
const maxParentDepth = 64
