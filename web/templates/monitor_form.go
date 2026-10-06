package templates

import (
	"maps"
	"slices"
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

// advancedFields are the inputs inside the collapsed Advanced section; it
// opens when one of them has an error.
var advancedFields = []string{"custom_user_agent", "max_body_kib", "tls_warning_days", "proxy_url", "ip_family"}

// AdvancedOpen reports whether the Advanced section starts open.
func (f MonitorForm) AdvancedOpen() bool {
	for _, k := range advancedFields {
		if _, ok := f.Errors[k]; ok {
			return true
		}
	}
	return false
}

// FormFieldOrder is the order of the error summary: the order of the page.
var FormFieldOrder = []string{
	"type", "name", "tags", "host", "port", "hostname", "query_type", "resolver", "expected", "match_mode",
	"expected_interval", "grace", "source_label", "url", "method", "request_body", "headers", "auth", "basic_user", "basic_password",
	"bearer_token", "sh_new_name", "sh_new_value", "secret_headers", "expected_status", "body_contains", "body_not_contains",
	"json_assertions", "interval", "timeout", "retry_delay", "failure_threshold", "success_threshold",
	"parent_monitor_id", "custom_user_agent", "max_body_kib", "tls_warning_days", "proxy_url", "ip_family",
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
	"success_threshold": "Successes before up", "parent_monitor_id": "Depends on",
	"custom_user_agent": "User-Agent", "max_body_kib": "Body limit", "tls_warning_days": "Warning days",
	"proxy_url": "Proxy", "ip_family": "IP version",
}

// summaryItem is one entry of the error summary.
type summaryItem struct {
	Field, Label, Message string
}

// summary lists the errors in page order; unknown keys come last.
func (f MonitorForm) summary() []summaryItem {
	var out []summaryItem
	seen := map[string]bool{}
	for _, k := range FormFieldOrder {
		if msg, ok := f.Errors[k]; ok {
			out = append(out, summaryItem{k, fieldLabels[k], msg})
			seen[k] = true
		}
	}
	for _, k := range slices.Sorted(maps.Keys(f.Errors)) {
		if !seen[k] && k != "form" {
			out = append(out, summaryItem{k, k, f.Errors[k]})
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
