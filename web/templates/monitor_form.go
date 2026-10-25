package templates

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MonitorForm is the state of the create/edit monitor page
// (docs/03_INFORMATION_ARCHITECTURE.md "Monitor creation/editing"). Numbers
// are kept as typed, so a rejected value is shown back as entered. Secret
// values are never part of it: the page only learns which secrets are
// stored, and their inputs always render empty.
type MonitorForm struct {
	ID      string // "" while creating
	Type    string // http, tcp, icmp, dns or heartbeat; fixed once created
	Name    string
	Tags    string // comma-separated
	Enabled bool   // create only: start checking at once

	Host string // tcp, icmp
	Port string // tcp

	DNSHostname string
	QueryType   string
	Resolver    string
	Expected    string // one value per line
	MatchMode   string // "all" or "any"

	ExpectedInterval string // heartbeat, seconds
	Grace            string // heartbeat, seconds
	SourceLabel      string

	URL             string
	Method          string
	RequestBody     string
	FollowRedirects bool
	Headers         string // one "Name: value" per line

	Auth          string // "", "basic" or "bearer"
	HasBasic      bool   // a basic-auth value is stored
	HasBearer     bool   // a bearer token is stored
	SecretHeaders []string

	ExpectedStatus  string
	BodyContains    string
	BodyNotContains string
	Assertions      []AssertionField

	Interval         string // seconds
	Timeout          string // seconds
	RetryDelay       string // seconds
	FailureThreshold string
	SuccessThreshold string

	Parent  string
	Parents []Option
	// Profile is the chosen notification profile; Profiles the choices.
	Profile  string
	Profiles []Option

	UserAgent  string
	MaxBodyKiB string
	TLSExpiry  bool
	TLSDays    string // "30, 14, 7"
	Insecure   bool
	Proxy      string
	IPFamily   string

	// Errors maps a field name (the input's name) to its message; "form"
	// holds a failure not tied to one field.
	Errors map[string]string
}

// AssertionField is one JSON assertion row. Value is the expected value as
// JSON text (a quoted string, a number, true, false or null).
type AssertionField struct {
	Path  string
	Op    string
	Value string
}

// Option is one choice of a select.
type Option struct {
	Value string
	Label string
}

// MonitorTypes are the types offered on the create page, in order.
var MonitorTypes = []Option{
	{"http", "HTTP(S)"},
	{"tcp", "TCP port"},
	{"icmp", "Ping"},
	{"dns", "DNS"},
	{"heartbeat", "Heartbeat"},
}

// typeCards say on the create page what each type checks, with its icon.
var typeCards = map[string]struct{ Icon, Text string }{
	"http":      {"globe", "A URL answers"},
	"tcp":       {"plug", "A port is open"},
	"icmp":      {"activity", "A host answers pings"},
	"dns":       {"at-sign", "A name resolves"},
	"heartbeat": {"heart-pulse", "Your job checks in"},
}

// TypeLabel names a monitor type for people.
func TypeLabel(typ string) string {
	for _, o := range MonitorTypes {
		if o.Value == typ {
			return o.Label
		}
	}
	return typ
}

// DNSQueryTypes are the DNS record types a monitor can query.
var DNSQueryTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS"}

// Editing reports whether the form edits an existing monitor.
func (f MonitorForm) Editing() bool { return f.ID != "" }

// Action is where the form posts.
func (f MonitorForm) Action() string {
	if f.Editing() {
		return "/monitors/" + f.ID
	}
	return "/monitors"
}

// FormSection is one collapsible row of the form, under the essentials:
// everything in it has a default, so it starts closed and its summary says
// what it holds.
type FormSection struct {
	Key, Title string
	fields     []string // the inputs inside; an error in one opens the section
}

var (
	sectionChecking   = FormSection{"checking", "Checking", []string{"interval", "timeout", "failure_threshold", "retry_delay", "success_threshold"}}
	sectionAssertions = FormSection{"assertions", "Assertions", []string{"expected_status", "body_contains", "body_not_contains", "json_assertions"}}
	sectionRequest    = FormSection{"request", "Request", []string{"method", "request_body", "follow_redirects", "headers", "auth",
		"basic_user", "basic_password", "bearer_token", "sh_new_name", "sh_new_value", "secret_headers"}}
	sectionAlerts   = FormSection{"alerts", "Alerts", []string{"notification_profile_id", "parent_monitor_id"}}
	sectionAdvanced = FormSection{"advanced", "Advanced", []string{"custom_user_agent", "max_body_kib", "tls_warning_days", "proxy_url", "ip_family"}}
	sectionQuery    = FormSection{"query", "Query", []string{"resolver", "expected", "match_mode"}}
	sectionSource   = FormSection{"source", "Source", []string{"source_label"}}
)

// formSections are the sections of each type, in page order.
var formSections = map[string][]FormSection{
	"http":      {sectionChecking, sectionAssertions, sectionRequest, sectionAlerts, sectionAdvanced},
	"tcp":       {sectionChecking, sectionAlerts},
	"icmp":      {sectionChecking, sectionAlerts},
	"dns":       {sectionQuery, sectionChecking, sectionAlerts},
	"heartbeat": {sectionSource, sectionChecking, sectionAlerts},
}

// Sections are the form's sections for its type, in page order.
func (f MonitorForm) Sections() []FormSection { return formSections[f.Type] }

// TestAction is where Test now posts: the edit address, so a secret left
// blank is read from the stored monitor.
func (f MonitorForm) TestAction() string { return f.Action() + "/test" }

// unitName spells a unit out for the label.
func unitName(unit string) string {
	if unit == "s" {
		return "seconds"
	}
	return unit
}

// IsFormSection reports whether key names a section.
func IsFormSection(key string) bool {
	for _, s := range formSections["http"] {
		if s.Key == key {
			return true
		}
	}
	return key == sectionQuery.Key || key == sectionSource.Key
}

// SectionOpen reports whether a section starts open: when one of its
// fields has an error, so the field and its message are visible.
func (f MonitorForm) SectionOpen(key string) bool {
	for _, s := range formSections[f.Type] {
		if s.Key != key {
			continue
		}
		for k := range f.Errors {
			if slices.Contains(s.fields, k) || (key == "request" && strings.HasPrefix(k, "sh_value.")) {
				return true
			}
		}
	}
	return false
}

// SectionSummary says in one line what a closed section holds, from the
// values as typed. Secrets appear only as the kind of authentication and
// a count.
func (f MonitorForm) SectionSummary(key string) string {
	var parts []string
	add := func(p ...string) { parts = append(parts, p...) }
	switch key {
	case "checking":
		if f.Type != "heartbeat" {
			add("every "+seconds(f.Interval), "timeout "+seconds(f.Timeout))
		}
		add("down after " + count(f.FailureThreshold, "failure", "failures"))
		if f.Type == "heartbeat" {
			add("retry after " + seconds(f.RetryDelay))
		}
		if n := strings.TrimSpace(f.SuccessThreshold); n != "" && n != "1" {
			add("up after " + count(n, "success", "successes"))
		}
	case "assertions":
		status := strings.TrimSpace(f.ExpectedStatus)
		if status == "" {
			status = "200-399"
		}
		add("status " + strings.ReplaceAll(status, "-", "–"))
		if b := strings.TrimSpace(f.BodyContains); b != "" {
			add("body contains “" + clipText(b, 20) + "”")
		}
		if b := strings.TrimSpace(f.BodyNotContains); b != "" {
			add("body lacks “" + clipText(b, 20) + "”")
		}
		n := 0
		for _, a := range f.Assertions {
			if strings.TrimSpace(a.Path) != "" {
				n++
			}
		}
		if n > 0 {
			add(strconv.Itoa(n) + " JSON " + plural(n, "check", "checks"))
		}
		if len(parts) == 1 {
			add("any body")
		}
	case "request":
		method := f.Method
		if method == "" {
			method = "GET"
		}
		add(method)
		if f.FollowRedirects {
			add("follows redirects")
		} else {
			add("no redirects")
		}
		if n := lines(f.Headers); n > 0 {
			add(strconv.Itoa(n) + " " + plural(n, "header", "headers"))
		}
		switch f.Auth {
		case "basic":
			add("basic auth")
		case "bearer":
			add("bearer token")
		}
		if n := len(f.SecretHeaders); n > 0 {
			add(strconv.Itoa(n) + " secret " + plural(n, "header", "headers"))
		}
	case "alerts":
		if p := optionLabel(f.Profiles, f.Profile); p != "" {
			add("notifies " + p)
		} else {
			add("no notifications")
		}
		if p := optionLabel(f.Parents, f.Parent); p != "" {
			add("depends on " + p)
		} else {
			add("no dependency")
		}
	case "advanced":
		if f.TLSExpiry {
			add("certificate warning " + strings.TrimSpace(f.TLSDays) + " days")
		} else {
			add("no certificate warning")
		}
		if f.Insecure {
			add("accepts invalid certificates")
		}
		if strings.TrimSpace(f.Proxy) != "" {
			add("through a proxy")
		}
		switch f.IPFamily {
		case "ipv4":
			add("IPv4 only")
		case "ipv6":
			add("IPv6 only")
		}
		if strings.TrimSpace(f.UserAgent) != "" {
			add("own User-Agent")
		}
		if kib := strings.TrimSpace(f.MaxBodyKiB); kib != "" && kib != "1024" {
			add("body limit " + kib + " KiB")
		}
	case "query":
		if r := strings.TrimSpace(f.Resolver); r != "" {
			add("resolver " + r)
		} else {
			add("this server's resolvers")
		}
		switch n := lines(f.Expected); {
		case n == 0:
			add("any answer counts")
		case n == 1:
			add("expects 1 value")
		case f.MatchMode == "any":
			add("expects any of " + strconv.Itoa(n) + " values")
		default:
			add("expects all " + strconv.Itoa(n) + " values")
		}
	case "source":
		if l := strings.TrimSpace(f.SourceLabel); l != "" {
			add("from " + clipText(l, 40))
		} else {
			add("no source label")
		}
	}
	s := strings.Join(parts, " · ")
	if r, n := utf8.DecodeRuneInString(s); n > 0 {
		s = strings.ToUpper(string(r)) + s[n:]
	}
	return s
}

// joinFacts joins a test result's facts for one line.
func joinFacts(fs []string) string { return strings.Join(fs, " · ") }

// seconds is "30 s" for a number of seconds as typed.
func seconds(v string) string { return strings.TrimSpace(v) + " s" }

// count is "1 failure" or "2 failures" for a number as typed.
func count(v, one, many string) string {
	if v = strings.TrimSpace(v); v == "1" {
		return v + " " + one
	}
	return v + " " + many
}

// clipText shortens s to at most n characters, ending in "…".
func clipText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n])) + "…"
}

// lines counts the non-blank lines of a textarea.
func lines(s string) int {
	n := 0
	for l := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

func optionLabel(opts []Option, value string) string {
	if value == "" {
		return ""
	}
	for _, o := range opts {
		if o.Value == value {
			return o.Label
		}
	}
	return ""
}

// FormFieldOrder is the order of the error summary: the order of the page.
var FormFieldOrder = []string{
	"type", "url", "host", "port", "hostname", "query_type", "expected_interval", "grace", "name", "tags",
	"resolver", "expected", "match_mode", "source_label",
	"interval", "timeout", "failure_threshold", "retry_delay", "success_threshold",
	"expected_status", "body_contains", "body_not_contains", "json_assertions",
	"method", "request_body", "headers", "auth", "basic_user", "basic_password",
	"bearer_token", "sh_new_name", "sh_new_value", "secret_headers",
	"notification_profile_id", "parent_monitor_id",
	"tls_warning_days", "custom_user_agent", "max_body_kib", "proxy_url", "ip_family",
}

// fieldLabels names the fields in the error summary.
var fieldLabels = map[string]string{
	"type": "Type", "name": "Name", "tags": "Tags", "host": "Host", "port": "Port", "hostname": "Host name",
	"query_type": "Record type", "resolver": "Resolver", "expected": "Expected values", "match_mode": "Match",
	"expected_interval": "Expected every", "grace": "Grace period", "source_label": "Source label", "url": "URL", "method": "Method", "request_body": "Request body",
	"headers": "Headers", "auth": "Authentication", "basic_user": "User name", "basic_password": "Password",
	"bearer_token": "Token", "sh_new_name": "New header name", "sh_new_value": "New header value",
	"secret_headers": "Secret headers", "expected_status": "Expected status",
	"body_contains": "Body contains", "body_not_contains": "Body does not contain",
	"json_assertions": "JSON assertions", "interval": "Interval", "timeout": "Timeout",
	"retry_delay": "Retry delay", "failure_threshold": "Failures before down",
	"success_threshold": "Successes before up", "parent_monitor_id": "Depends on", "notification_profile_id": "Notification profile",
	"custom_user_agent": "User-Agent", "max_body_kib": "Body limit", "tls_warning_days": "Warning days",
	"proxy_url": "Proxy", "ip_family": "IP version",
}

// FormProblem is one entry of the error summary: the field's id, its name
// and what is wrong.
type FormProblem struct {
	Field, Label, Message string
}

// summaryItem is the error summary entry of every form page.
type summaryItem = FormProblem

// summary lists the errors in page order; unknown keys come last.
func (f MonitorForm) summary() []FormProblem { return f.Problems() }

// Problems lists the errors in page order; unknown keys come last.
func (f MonitorForm) Problems() []FormProblem {
	var out []FormProblem
	seen := map[string]bool{}
	for _, k := range FormFieldOrder {
		if msg, ok := f.Errors[k]; ok {
			out = append(out, FormProblem{k, fieldLabels[k], msg})
			seen[k] = true
		}
	}
	for _, k := range slices.Sorted(maps.Keys(f.Errors)) {
		if !seen[k] && k != "form" {
			out = append(out, FormProblem{k, k, f.Errors[k]})
		}
	}
	return out
}

// keepHint is the id of the "leave blank to keep" note of a secret input,
// or "" when nothing is stored.
func keepHint(stored bool, name string) string {
	if stored {
		return name + "-keep"
	}
	return ""
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// JSONOps are the assertion operators offered (docs/06).
var JSONOps = []Option{
	{"equals", "equals"},
	{"not_equals", "does not equal"},
	{"exists", "exists"},
	{"not_exists", "does not exist"},
}

// TestResult is the outcome of Test now: the problems that kept it from
// running, an error, or the check's result.
type TestResult struct {
	Problems []FormProblem // invalid input: nothing ran
	Error    string        // the check could not run
	Success  bool
	Kind     string   // failure kind, in words
	Facts    []string // "HTTP 200", "266 ms", the certificate
	Message  string
	Snippet  string
}
