package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/audit"
	"github.com/drilonrecica/sinjal/internal/monitor"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/web/sse"
	"github.com/drilonrecica/sinjal/web/templates"
)

// The create/edit monitor page (docs/03 "Monitor creation/editing",
// docs/38). The form speaks in the units people think in (seconds, KiB,
// "Name: value" lines); this file translates between it and
// store.MonitorInput. Secret values only travel from the browser to the
// store: inputs render empty and blank means "keep".

// monitorFormMaxBody bounds a posted form: a 64 KiB request body plus
// the rest of the fields with room to spare.
const monitorFormMaxBody = 512 << 10

// RegisterMonitorChanges mounts the create and edit forms, the pause,
// resume and delete actions and the heartbeat token regeneration, which
// also needs a recent re-authentication (recent). They must sit behind
// RequireAdmin: the form shows the full configuration, and every one of
// them changes monitors.
func RegisterMonitorChanges(r chi.Router, h *Monitors, recent func(http.Handler) http.Handler) {
	r.With(recent).Post("/monitors/{id}/heartbeat/token", h.regenerateToken)
	r.Get("/monitors/new", h.newForm)
	r.Post("/monitors", h.create)
	r.Get("/monitors/{id}/edit", h.editForm)
	r.Post("/monitors/{id}", h.update)
	r.Post("/monitors/{id}/pause", h.pause)
	r.Post("/monitors/{id}/resume", h.resume)
	r.Post("/monitors/{id}/delete", h.remove)
}

// Form field names whose store rule key differs (the form uses other units).
var storeFieldToForm = map[string]string{
	"interval_seconds":          "interval",
	"timeout_ms":                "timeout",
	"retry_delay_ms":            "retry_delay",
	"max_body_bytes":            "max_body_kib",
	"expected_interval_seconds": "expected_interval",
	"grace_seconds":             "grace",
}

// monitorConfig is the stored config of a monitor; only the field of its
// type is set.
type monitorConfig struct {
	HTTP      store.HTTPConfig
	TCP       store.TCPConfig
	ICMP      store.ICMPConfig
	DNS       store.DNSConfig
	Heartbeat store.HeartbeatConfig
}

// loadConfig reads the config of m's type.
func loadConfig(ctx context.Context, q *sql.DB, m store.Monitor) (monitorConfig, error) {
	var c monitorConfig
	var err error
	switch m.Type {
	case store.TypeHTTP:
		c.HTTP, err = store.GetHTTPConfig(ctx, q, m.ID)
	case store.TypeTCP:
		c.TCP, err = store.GetTCPConfig(ctx, q, m.ID)
	case store.TypeICMP:
		c.ICMP, err = store.GetICMPConfig(ctx, q, m.ID)
	case store.TypeDNS:
		c.DNS, err = store.GetDNSConfig(ctx, q, m.ID)
	case store.TypeHeartbeat:
		c.Heartbeat, err = store.GetHeartbeatConfig(ctx, q, m.ID)
	default:
		err = store.ErrNotFound
	}
	return c, err
}

// formType is typ when it is a monitor type, else HTTP.
func formType(typ string) string {
	if slices.Contains(store.Types, typ) {
		return typ
	}
	return store.TypeHTTP
}

// newMonitorForm is the form for a new monitor of type typ (HTTP when it
// is not a type), prefilled with the defaults.
func newMonitorForm(typ string) templates.MonitorForm {
	return formFromMonitor(store.Monitor{
		Type:             formType(typ),
		Enabled:          true,
		IntervalSeconds:  store.DefaultIntervalSeconds,
		TimeoutMS:        store.DefaultTimeoutMS,
		RetryDelayMS:     store.DefaultRetryDelayMS,
		FailureThreshold: store.DefaultFailureThreshold,
		SuccessThreshold: store.DefaultSuccessThreshold,
	}, monitorConfig{
		HTTP: store.HTTPConfig{
			Method:           "GET",
			FollowRedirects:  true,
			ExpectedStatus:   store.DefaultExpectedStatus,
			MaxBodyBytes:     store.DefaultMaxBodyBytes,
			TLSExpiryEnabled: true,
			TLSWarningDays:   store.DefaultTLSWarningDays,
		},
		DNS: store.DNSConfig{QueryType: "A", MatchMode: "all"},
	}, nil)
}

// formFromMonitor fills the form from stored values. Secrets are added by
// the caller (names only).
func formFromMonitor(m store.Monitor, cfg monitorConfig, tags []string) templates.MonitorForm {
	c := cfg.HTTP
	f := templates.MonitorForm{
		ID:               m.ID,
		Type:             m.Type,
		Name:             m.Name,
		Tags:             strings.Join(tags, ", "),
		Enabled:          m.Enabled,
		URL:              c.URL,
		Method:           c.Method,
		RequestBody:      c.RequestBody,
		FollowRedirects:  c.FollowRedirects,
		ExpectedStatus:   c.ExpectedStatus,
		BodyContains:     c.BodyContains,
		BodyNotContains:  c.BodyNotContains,
		Interval:         strconv.Itoa(m.IntervalSeconds),
		Timeout:          formatSeconds(m.TimeoutMS),
		RetryDelay:       formatSeconds(m.RetryDelayMS),
		FailureThreshold: strconv.Itoa(m.FailureThreshold),
		SuccessThreshold: strconv.Itoa(m.SuccessThreshold),
		Parent:           m.ParentMonitorID,
		Profile:          m.NotificationProfileID,
		UserAgent:        c.CustomUserAgent,
		MaxBodyKiB:       strconv.Itoa(c.MaxBodyBytes / 1024),
		TLSExpiry:        c.TLSExpiryEnabled,
		Insecure:         c.InsecureSkipVerify,
		Proxy:            c.ProxyURL,
		IPFamily:         c.IPFamily,
	}
	switch m.Type {
	case store.TypeTCP:
		f.Host, f.Port = cfg.TCP.Host, strconv.Itoa(cfg.TCP.Port)
	case store.TypeICMP:
		f.Host = cfg.ICMP.Host
	case store.TypeDNS:
		d := cfg.DNS
		f.DNSHostname, f.QueryType, f.Resolver, f.MatchMode = d.Hostname, d.QueryType, d.Resolver, d.MatchMode
		f.Expected = strings.Join(d.Expected, "\n")
	case store.TypeHeartbeat:
		h := cfg.Heartbeat
		if h.ExpectedInterval > 0 {
			f.ExpectedInterval = strconv.Itoa(int(h.ExpectedInterval.Seconds()))
		}
		f.Grace = strconv.Itoa(int(h.Grace.Seconds()))
		f.SourceLabel = h.SourceLabel
	}
	if hs, err := monitor.ParseHeaders(c.Headers); err == nil {
		lines := make([]string, len(hs))
		for i, h := range hs {
			lines[i] = h.Name + ": " + h.Value
		}
		f.Headers = strings.Join(lines, "\n")
	}
	var days []int
	if json.Unmarshal([]byte(c.TLSWarningDays), &days) == nil {
		parts := make([]string, len(days))
		for i, d := range days {
			parts[i] = strconv.Itoa(d)
		}
		f.TLSDays = strings.Join(parts, ", ")
	}
	if as, err := monitor.ParseJSONAssertions(c.JSONAssertions); err == nil {
		for _, a := range as {
			f.Assertions = append(f.Assertions, templates.AssertionField{Path: a.Path, Op: a.Op, Value: string(a.Value)})
		}
	}
	return f
}

// formatSeconds shows milliseconds as seconds without trailing zeros.
func formatSeconds(ms int) string {
	return strconv.FormatFloat(float64(ms)/1000, 'f', -1, 64)
}

// maxAssertionRows bounds how many assertion rows a post is read for; the
// store allows store.MaxJSONAssertions of them.
const maxAssertionRows = store.MaxJSONAssertions + 10

// padAssertions adds blank rows to fill in: three, or up to the limit.
// The page renders through renderForm, which pads once.
func padAssertions(rows []templates.AssertionField) []templates.AssertionField {
	n := max(len(rows), min(len(rows)+3, store.MaxJSONAssertions))
	for len(rows) < n {
		rows = append(rows, templates.AssertionField{Op: monitor.OpEquals})
	}
	return rows
}

// formFromValues echoes a posted form as typed.
func formFromValues(v url.Values) templates.MonitorForm {
	f := templates.MonitorForm{
		Type:             formType(v.Get("type")),
		Name:             v.Get("name"),
		Tags:             v.Get("tags"),
		Enabled:          v.Get("enabled") != "",
		URL:              v.Get("url"),
		Method:           v.Get("method"),
		RequestBody:      v.Get("request_body"),
		FollowRedirects:  v.Get("follow_redirects") != "",
		Headers:          v.Get("headers"),
		Auth:             v.Get("auth"),
		ExpectedStatus:   v.Get("expected_status"),
		BodyContains:     v.Get("body_contains"),
		BodyNotContains:  v.Get("body_not_contains"),
		Interval:         v.Get("interval"),
		Timeout:          v.Get("timeout"),
		RetryDelay:       v.Get("retry_delay"),
		FailureThreshold: v.Get("failure_threshold"),
		SuccessThreshold: v.Get("success_threshold"),
		Parent:           v.Get("parent_monitor_id"),
		Profile:          v.Get("notification_profile_id"),
		UserAgent:        v.Get("custom_user_agent"),
		MaxBodyKiB:       v.Get("max_body_kib"),
		TLSExpiry:        v.Get("tls_expiry") != "",
		TLSDays:          v.Get("tls_warning_days"),
		Insecure:         v.Get("insecure_skip_verify") != "",
		Proxy:            v.Get("proxy_url"),
		IPFamily:         v.Get("ip_family"),
		Host:             v.Get("host"),
		Port:             v.Get("port"),
		DNSHostname:      v.Get("hostname"),
		QueryType:        v.Get("query_type"),
		Resolver:         v.Get("resolver"),
		Expected:         v.Get("expected"),
		MatchMode:        v.Get("match_mode"),
		ExpectedInterval: v.Get("expected_interval"),
		Grace:            v.Get("grace"),
		SourceLabel:      v.Get("source_label"),
		Errors:           map[string]string{},
	}
	for i := range maxAssertionRows {
		n := strconv.Itoa(i)
		if p := strings.TrimSpace(v.Get("json_path_" + n)); p != "" {
			f.Assertions = append(f.Assertions, templates.AssertionField{
				Path: p, Op: v.Get("json_op_" + n), Value: strings.TrimSpace(v.Get("json_value_" + n)),
			})
		}
	}
	return f
}

// monitorFromForm converts the form into the store's input. Values that
// cannot even be parsed get a message in f.Errors and leave their field
// at zero; the store's own rules run afterwards.
func monitorFromForm(f templates.MonitorForm) store.MonitorInput {
	errs := f.Errors
	in := store.MonitorInput{
		Type:                  f.Type,
		Name:                  f.Name,
		Enabled:               f.Enabled,
		IntervalSeconds:       wholeNumber(f.Interval, "interval", errs),
		TimeoutMS:             milliseconds(f.Timeout, "timeout", errs),
		RetryDelayMS:          milliseconds(f.RetryDelay, "retry_delay", errs),
		FailureThreshold:      wholeNumber(f.FailureThreshold, "failure_threshold", errs),
		SuccessThreshold:      wholeNumber(f.SuccessThreshold, "success_threshold", errs),
		ParentMonitorID:       f.Parent,
		NotificationProfileID: f.Profile,
		HTTP: store.HTTPConfig{
			URL:                f.URL,
			Method:             f.Method,
			FollowRedirects:    f.FollowRedirects,
			ExpectedStatus:     strings.TrimSpace(f.ExpectedStatus),
			BodyContains:       f.BodyContains,
			BodyNotContains:    f.BodyNotContains,
			RequestBody:        f.RequestBody,
			CustomUserAgent:    strings.TrimSpace(f.UserAgent),
			TLSExpiryEnabled:   f.TLSExpiry,
			InsecureSkipVerify: f.Insecure,
			ProxyURL:           strings.TrimSpace(f.Proxy),
			IPFamily:           f.IPFamily,
		},
	}
	for t := range strings.SplitSeq(f.Tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			in.Tags = append(in.Tags, t)
		}
	}
	if in.RetryDelayMS == 0 && strings.TrimSpace(f.RetryDelay) != "" {
		// Zero would silently become the default.
		addErr(errs, "retry_delay", "Use a delay above zero.")
	}
	if kib := wholeNumber(f.MaxBodyKiB, "max_body_kib", errs); kib != 0 {
		if kib < 0 || kib > store.MaxMaxBodyBytes/1024 {
			addErr(errs, "max_body_kib", "Use a body limit from 1 to 1024 KiB.")
		} else {
			in.HTTP.MaxBodyBytes = kib * 1024
		}
	}
	switch f.Type {
	case store.TypeHTTP:
		in.HTTP.Headers = headersJSON(f.Headers, errs)
		in.HTTP.TLSWarningDays = tlsDaysJSON(f.TLSDays, errs)
		in.HTTP.JSONAssertions = assertionsJSON(f.Assertions)
	case store.TypeTCP:
		in.TCP = store.TCPConfig{Host: f.Host, Port: wholeNumber(f.Port, "port", errs)}
	case store.TypeICMP:
		in.ICMP = store.ICMPConfig{Host: f.Host}
	case store.TypeDNS:
		in.DNS = store.DNSConfig{Hostname: f.DNSHostname, QueryType: f.QueryType, Resolver: f.Resolver, MatchMode: f.MatchMode}
		// TXT values are compared exactly, so only the line ending goes.
		for line := range strings.SplitSeq(f.Expected, "\n") {
			if line = strings.TrimSuffix(line, "\r"); strings.TrimSpace(line) != "" {
				in.DNS.Expected = append(in.DNS.Expected, line)
			}
		}
	case store.TypeHeartbeat:
		in.Heartbeat = store.HeartbeatSettings{
			ExpectedIntervalSeconds: wholeNumber(f.ExpectedInterval, "expected_interval", errs),
			GraceSeconds:            wholeNumber(f.Grace, "grace", errs),
			SourceLabel:             f.SourceLabel,
		}
	}
	return in
}

func addErr(errs map[string]string, field, msg string) {
	if _, ok := errs[field]; !ok {
		errs[field] = msg
	}
}

// wholeNumber parses an optional whole number; blank is 0 (the default).
func wholeNumber(s, field string, errs map[string]string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n > 1e9 || n < -1e9 {
		addErr(errs, field, "Enter a whole number.")
		return 0
	}
	return n
}

// milliseconds parses optional seconds, decimals allowed; blank is 0.
func milliseconds(s, field string, errs map[string]string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.Abs(f) > 1e6 {
		addErr(errs, field, "Enter a number of seconds, such as 5 or 0.5.")
		return 0
	}
	return int(math.Round(f * 1000))
}

// headersJSON turns "Name: value" lines into headers_json.
func headersJSON(text string, errs map[string]string) string {
	var hs []monitor.Header
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "" {
			addErr(errs, "headers", fmt.Sprintf("Line %d: write each header as Name: value.", i+1))
			return ""
		}
		hs = append(hs, monitor.Header{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
	}
	if len(hs) == 0 {
		return ""
	}
	b, _ := json.Marshal(hs)
	return string(b)
}

// tlsDaysJSON turns "30, 14, 7" into a JSON list; blank is the default.
func tlsDaysJSON(text string, errs map[string]string) string {
	fields := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ' ' })
	if len(fields) == 0 {
		return ""
	}
	days := make([]int, 0, len(fields))
	for _, s := range fields {
		d, err := strconv.Atoi(s)
		if err != nil {
			addErr(errs, "tls_warning_days", "Use whole days separated by commas, such as 30, 14, 7.")
			return ""
		}
		days = append(days, d)
	}
	b, _ := json.Marshal(days)
	return string(b)
}

// assertionsJSON turns the rows into json_assertions_json. A value that is
// a JSON scalar is kept as typed; anything else is taken as a string, so
// ok and "ok" mean the same. The store validates the result.
func assertionsJSON(rows []templates.AssertionField) string {
	var as []monitor.JSONAssertion
	for _, r := range rows {
		a := monitor.JSONAssertion{Path: r.Path, Op: r.Op}
		if v := strings.TrimSpace(r.Value); v != "" {
			if json.Valid([]byte(v)) && v[0] != '{' && v[0] != '[' {
				a.Value = json.RawMessage(v)
			} else {
				a.Value, _ = json.Marshal(v)
			}
		}
		as = append(as, a)
	}
	if len(as) == 0 {
		return ""
	}
	b, _ := json.Marshal(as)
	return string(b)
}

// secretEdits is what a post asks to change among a monitor's secrets.
// Empty values mean "keep".
type secretEdits struct {
	auth         string // "", "basic" or "bearer"
	basic        string // "user:password" to store
	bearer       string
	headerSet    map[string]string // secret name (header.X) → new value
	headerRemove []string          // secret names
}

// secretsFromValues reads the secret inputs. stored holds the names of the
// secrets the monitor has; plain is the parsed headers_json, which may not
// repeat a secret header.
func secretsFromValues(v url.Values, stored map[string]bool, plain string, errs map[string]string) secretEdits {
	s := secretEdits{auth: v.Get("auth"), headerSet: map[string]string{}}
	switch s.auth {
	case "":
	case "basic":
		user, pass := v.Get("basic_user"), v.Get("basic_password")
		switch {
		case user == "" && pass == "":
			if !stored[monitor.SecretBasicAuth] {
				addErr(errs, "basic_user", "Enter a user name and password.")
			}
		case user == "" || pass == "":
			addErr(errs, "basic_user", "Enter both the user name and the password.")
		case strings.Contains(user, ":"):
			addErr(errs, "basic_user", "A user name cannot contain a colon.")
		case !monitor.ValidHeaderValue(user) || !monitor.ValidHeaderValue(pass):
			addErr(errs, "basic_user", "Remove control characters.")
		default:
			s.basic = user + ":" + pass
		}
	case "bearer":
		token := v.Get("bearer_token")
		switch {
		case token == "":
			if !stored[monitor.SecretBearerToken] {
				addErr(errs, "bearer_token", "Enter the token.")
			}
		case !monitor.ValidHeaderValue(token):
			addErr(errs, "bearer_token", "Remove control characters.")
		default:
			s.bearer = token
		}
	default:
		addErr(errs, "auth", "Choose none, basic or bearer.")
	}

	plainNames := map[string]bool{}
	if hs, err := monitor.ParseHeaders(plain); err == nil {
		for _, h := range hs {
			plainNames[strings.ToLower(h.Name)] = true
		}
	}
	for name := range stored {
		header, ok := strings.CutPrefix(name, monitor.SecretHeaderPrefix)
		if !ok {
			continue
		}
		switch {
		case v.Get("sh_remove."+header) != "":
			s.headerRemove = append(s.headerRemove, name)
		case plainNames[strings.ToLower(header)]:
			addErr(errs, "headers", "The header "+header+" is also a secret header.")
		case v.Get("sh_value."+header) != "":
			val := v.Get("sh_value." + header)
			if !monitor.ValidHeaderValue(val) {
				addErr(errs, "secret_headers", "The value of "+header+" contains control characters.")
			}
			s.headerSet[name] = val
		}
	}
	newName, newValue := strings.TrimSpace(v.Get("sh_new_name")), v.Get("sh_new_value")
	if newName != "" || newValue != "" {
		lower := strings.ToLower(newName)
		switch {
		case !monitor.ValidHeaderName(newName):
			addErr(errs, "sh_new_name", "Enter a header name such as X-Api-Key.")
		case lower == "authorization" || lower == "proxy-authorization" || lower == "host":
			addErr(errs, "sh_new_name", "Use the authentication setting for "+newName+".")
		case plainNames[lower]:
			addErr(errs, "sh_new_name", "This header is already set above.")
		case newValue == "":
			addErr(errs, "sh_new_value", "Enter the value.")
		case !monitor.ValidHeaderValue(newValue):
			addErr(errs, "sh_new_value", "Remove control characters.")
		default:
			name := monitor.SecretHeaderPrefix + newName
			if stored[name] {
				addErr(errs, "sh_new_name", "This secret header exists; enter its new value above.")
			}
			s.headerSet[name] = newValue
		}
	}
	return s
}

// applySecrets stores the edits. The method that is not chosen is removed
// first, because a monitor cannot hold basic and bearer auth together.
func (h *Monitors) applySecrets(r *http.Request, id string, s secretEdits) error {
	ctx, now := r.Context(), h.now()
	var drop []string
	switch s.auth {
	case "":
		drop = []string{monitor.SecretBasicAuth, monitor.SecretBearerToken}
	case "basic":
		drop = []string{monitor.SecretBearerToken}
	case "bearer":
		drop = []string{monitor.SecretBasicAuth}
	}
	for _, name := range append(drop, s.headerRemove...) {
		if err := store.DeleteSecret(ctx, h.db, id, name); err != nil {
			return err
		}
	}
	set := map[string]string{monitor.SecretBasicAuth: s.basic, monitor.SecretBearerToken: s.bearer}
	for name, v := range s.headerSet {
		set[name] = v
	}
	for name, v := range set {
		if v == "" {
			continue
		}
		if err := store.SetSecret(ctx, h.db, h.key, id, name, []byte(v), now); err != nil {
			return err
		}
	}
	return nil
}

// storedSecrets returns the secret names of a monitor as a set.
func (h *Monitors) storedSecrets(r *http.Request, id string) (map[string]bool, error) {
	out := map[string]bool{}
	if id == "" {
		return out, nil
	}
	names, err := store.SecretNames(r.Context(), h.db.Reader, id)
	for _, n := range names {
		out[n] = true
	}
	return out, err
}

// renderForm fills in what the page needs besides the field values (the
// parent choices and the names of stored secrets) and renders it.
func (h *Monitors) renderForm(w http.ResponseWriter, r *http.Request, status int, f templates.MonitorForm, stored map[string]bool) {
	monitors, err := store.ListMonitors(r.Context(), h.db.Reader)
	if err != nil {
		h.fail(w, r, "listing monitors", err)
		return
	}
	for _, m := range monitors {
		if m.ID != f.ID {
			f.Parents = append(f.Parents, templates.Option{Value: m.ID, Label: m.Name})
		}
	}
	profiles, err := store.ListProfiles(r.Context(), h.db.Reader)
	if err != nil {
		h.fail(w, r, "listing profiles", err)
		return
	}
	f.Profiles = nil
	for _, p := range profiles {
		f.Profiles = append(f.Profiles, templates.Option{Value: p.ID, Label: p.Name})
	}
	f.HasBasic, f.HasBearer = stored[monitor.SecretBasicAuth], stored[monitor.SecretBearerToken]
	f.SecretHeaders = nil
	for name := range stored {
		if header, ok := strings.CutPrefix(name, monitor.SecretHeaderPrefix); ok {
			f.SecretHeaders = append(f.SecretHeaders, header)
		}
	}
	slices.Sort(f.SecretHeaders)
	f.Assertions = padAssertions(f.Assertions)
	title := "Create monitor — Sinjal"
	if f.Editing() {
		title = "Edit " + f.Name + " — Sinjal"
	}
	render(w, r, h.log, status, templates.MonitorFormPage(pageFor(r, title), f))
}

func (h *Monitors) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.log.Error("monitors: "+what+" failed", "route", r.URL.Path, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// newForm serves GET /monitors/new.
func (h *Monitors) newForm(w http.ResponseWriter, r *http.Request) {
	h.renderForm(w, r, http.StatusOK, newMonitorForm(r.URL.Query().Get("type")), nil)
}

// editForm serves GET /monitors/{id}/edit.
func (h *Monitors) editForm(w http.ResponseWriter, r *http.Request) {
	ctx, q, id := r.Context(), h.db.Reader, chi.URLParam(r, "id")
	m, err := store.GetMonitor(ctx, q, id)
	var c monitorConfig
	var tags []string
	if err == nil {
		c, err = loadConfig(ctx, q, m)
	}
	if err == nil {
		tags, err = store.MonitorTags(ctx, q, id)
	}
	stored := map[string]bool{}
	if err == nil {
		stored, err = h.storedSecrets(r, id)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "loading a monitor", err)
		return
	}
	f := formFromMonitor(m, c, tags)
	switch {
	case stored[monitor.SecretBasicAuth]:
		f.Auth = "basic"
	case stored[monitor.SecretBearerToken]:
		f.Auth = "bearer"
	}
	h.renderForm(w, r, http.StatusOK, f, stored)
}

// create serves POST /monitors.
func (h *Monitors) create(w http.ResponseWriter, r *http.Request) {
	h.save(w, r, nil)
}

// update serves POST /monitors/{id}.
func (h *Monitors) update(w http.ResponseWriter, r *http.Request) {
	m, err := store.GetMonitor(r.Context(), h.db.Reader, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		h.fail(w, r, "loading a monitor", err)
		return
	}
	h.save(w, r, &m)
}

// save validates a posted form completely, then writes the monitor and its
// secrets, schedules it and announces it. old is nil for a new monitor;
// an existing one keeps its type whatever the post says. A new heartbeat
// monitor gets its token here, and the page that follows shows it once.
func (h *Monitors) save(w http.ResponseWriter, r *http.Request, old *store.Monitor) {
	cs, _ := SessionFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, monitorFormMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	id := ""
	if old != nil {
		id = old.ID
	}
	stored, err := h.storedSecrets(r, id)
	if err != nil {
		h.fail(w, r, "reading secret names", err)
		return
	}
	f := formFromValues(r.PostForm)
	if old != nil {
		f.ID, f.Type = old.ID, old.Type
	}
	in := monitorFromForm(f)
	var secrets secretEdits
	if in.Type == store.TypeHTTP {
		secrets = secretsFromValues(r.PostForm, stored, in.HTTP.Headers, f.Errors)
	}

	invalid := func(err error) bool {
		var fe store.FieldErrors
		if !errors.As(err, &fe) {
			return false
		}
		for k, msg := range fe {
			if mapped, ok := storeFieldToForm[k]; ok {
				k = mapped
			}
			addErr(f.Errors, k, msg)
		}
		return true
	}
	if len(f.Errors) > 0 {
		// Show the store's findings for the other fields too.
		if err := store.CheckMonitor(ctx, h.db.Reader, id, in); err != nil && !invalid(err) {
			h.fail(w, r, "validating a monitor", err)
			return
		}
		h.renderForm(w, r, http.StatusUnprocessableEntity, f, stored)
		return
	}

	var token string
	if old == nil {
		if in.Type == store.TypeHeartbeat {
			if token, in.Heartbeat.TokenHash, err = store.NewHeartbeatToken(); err != nil {
				h.fail(w, r, "issuing a heartbeat token", err)
				return
			}
		}
		id, err = store.CreateMonitor(ctx, h.db, in, h.now())
	} else {
		err = store.UpdateMonitor(ctx, h.db, id, in, h.now())
	}
	switch {
	case invalid(err):
		h.renderForm(w, r, http.StatusUnprocessableEntity, f, stored)
		return
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "saving a monitor", err)
		return
	}
	// The secrets go in before the monitor is scheduled, so its first
	// check already sends them. Only HTTP monitors have any.
	if in.Type == store.TypeHTTP {
		if err := h.applySecrets(r, id, secrets); err != nil {
			h.fail(w, r, "storing monitor secrets", err)
			return
		}
	}
	if err := h.engine.Schedule(ctx, id); err != nil {
		h.log.Error("monitor saved but not scheduled", "monitor_id", id, "error", err)
	}
	if f.Editing() {
		h.events.Publish(sse.MonitorUpdated, id)
		h.log.Info("monitor updated", "user_id", cs.User.ID, "monitor_id", id)
	} else {
		h.events.Publish(sse.MonitorCreated, id)
		h.log.Info("monitor created", "user_id", cs.User.ID, "monitor_id", id)
		h.audit(r, audit.MonitorCreated, id, in.Name)
	}
	if token != "" {
		h.showToken(w, r, id, in.Name, token, false)
		return
	}
	http.Redirect(w, r, "/monitors/"+id, http.StatusSeeOther)
}

// audit records a monitor event after the change it describes has been
// committed. A failure is logged; the change stands.
func (h *Monitors) audit(r *http.Request, typ, id, name string) {
	cs, _ := SessionFromContext(r.Context())
	ev := audit.Event{UserID: cs.User.ID, Type: typ, ObjectType: "monitor", ObjectID: id, Metadata: map[string]string{"name": name}}
	if err := audit.Record(r.Context(), h.db, ev, h.now()); err != nil {
		h.log.Error("audit event not written", "event", typ, "monitor_id", id, "error", err)
	}
}
