package web

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/audit"
	"github.com/drilonrecica/sinjal/internal/monitor"
	"github.com/drilonrecica/sinjal/internal/monitor/httpcheck"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/web/sse"
	"github.com/drilonrecica/sinjal/web/templates"
)

// The monitor detail page (docs/03 "Monitor detail") and the admin actions
// on it. Viewers see the page with the same rule as the list: no checked
// address, and on the Diagnostics tab no error messages or response
// excerpts, which can name internal hosts as well.

// detail serves GET /monitors/{id}.
func (h *Monitors) detail(w http.ResponseWriter, r *http.Request) {
	ctx, q, id := r.Context(), h.db.Reader, chi.URLParam(r, "id")
	m, mv, err := h.view(r, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "loading a monitor", err)
		return
	}
	v := templates.MonitorDetailView{
		Monitor: mv,
		Admin:   isAdmin(r),
		Paused:  mv.State == templates.StatePaused,
		Tab:     r.URL.Query().Get("tab"),
	}
	if !templates.IsDetailTab(v.Tab) {
		v.Tab = templates.DetailTabs[0].Key
	}
	now := h.now()
	switch v.Tab {
	case "overview":
		v.Overview = overviewFacts(m, now)
		v.History, err = h.historyView(r, m, false)
	case "history":
		v.History, err = h.historyView(r, m, true)
	case "incidents":
		v.Incidents, err = incidentListView(ctx, q, h.loc, now, id)
	case "configuration":
		v.Config, err = h.configGroups(r, m, mv.Parent, v.Admin)
	case "diagnostics":
		var rs []store.CheckResult
		rs, err = store.RecentFailures(ctx, q, id, store.MaxRecentFailures)
		for _, res := range rs {
			v.Failures = append(v.Failures, failureView(res, v.Admin))
		}
	}
	if err != nil {
		h.fail(w, r, "loading a monitor tab", err)
		return
	}
	render(w, r, h.log, http.StatusOK, templates.MonitorDetail(pageFor(r, m.Name+" — Sinjal"), v))
}

// overviewFacts are the facts under the header that do not change with
// every check.
func overviewFacts(m store.Monitor, now time.Time) []templates.Fact {
	ago := func(t *time.Time) string {
		if t == nil {
			return "never"
		}
		return formatSince(now.Sub(*t)) + " ago"
	}
	fs := []templates.Fact{
		{Label: "Checked every", Value: formatSince(time.Duration(m.IntervalSeconds) * time.Second)},
		{Label: "Last success", Value: ago(m.LastSuccessAt)},
		{Label: "Last failure", Value: ago(m.LastFailureAt)},
	}
	if m.TLSNotAfter != nil {
		days := int(math.Floor(m.TLSNotAfter.Sub(now).Hours() / 24))
		when := "expired"
		if days >= 0 {
			when = "in " + plural(days, "day")
		}
		fs = append(fs, templates.Fact{Label: "Certificate expires", Value: m.TLSNotAfter.UTC().Format("2006-01-02") + " (" + when + ")"})
	}
	return append(fs, templates.Fact{Label: "Created", Value: m.CreatedAt.UTC().Format("2006-01-02")})
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// configGroups is the Configuration tab. Viewers get the timing only.
func (h *Monitors) configGroups(r *http.Request, m store.Monitor, parent string, admin bool) ([]templates.ConfigGroup, error) {
	ctx, q := r.Context(), h.db.Reader
	tags, err := store.MonitorTags(ctx, q, m.ID)
	if err != nil {
		return nil, err
	}
	orNone := func(s, none string) string {
		if s == "" {
			return none
		}
		return s
	}
	seconds := func(ms int) string { return formatSeconds(ms) + " s" }
	gs := []templates.ConfigGroup{{Title: "Checking", Facts: []templates.Fact{
		{Label: "Type", Value: strings.ToUpper(m.Type)},
		{Label: "Interval", Value: strconv.Itoa(m.IntervalSeconds) + " s"},
		{Label: "Timeout", Value: seconds(m.TimeoutMS)},
		{Label: "Failures before down", Value: strconv.Itoa(m.FailureThreshold)},
		{Label: "Retry delay", Value: seconds(m.RetryDelayMS)},
		{Label: "Successes before up", Value: strconv.Itoa(m.SuccessThreshold)},
		{Label: "Depends on", Value: orNone(parent, "Nothing")},
		{Label: "Tags", Value: orNone(strings.Join(tags, ", "), "None")},
	}}}
	if !admin || m.Type != "http" {
		return gs, nil
	}

	c, err := store.GetHTTPConfig(ctx, q, m.ID)
	if err != nil {
		return nil, err
	}
	names, err := store.SecretNames(ctx, q, m.ID)
	if err != nil {
		return nil, err
	}
	authLabel, secretHeaders := "None", []string{}
	for _, n := range names {
		switch {
		case n == monitor.SecretBasicAuth:
			authLabel = "Basic (stored encrypted)"
		case n == monitor.SecretBearerToken:
			authLabel = "Bearer token (stored encrypted)"
		case strings.HasPrefix(n, monitor.SecretHeaderPrefix):
			secretHeaders = append(secretHeaders, strings.TrimPrefix(n, monitor.SecretHeaderPrefix))
		}
	}
	var headers []string
	if hs, err := monitor.ParseHeaders(c.Headers); err == nil {
		for _, hd := range hs {
			headers = append(headers, hd.Name+": "+hd.Value)
		}
	}
	var assertions []string
	if as, err := monitor.ParseJSONAssertions(c.JSONAssertions); err == nil {
		for _, a := range as {
			assertions = append(assertions, strings.TrimSpace(a.Path+" "+strings.ReplaceAll(a.Op, "_", " ")+" "+string(a.Value)))
		}
	}
	yesNo := func(b bool, yes, no string) string {
		if b {
			return yes
		}
		return no
	}
	tlsDays := "Off"
	if c.TLSExpiryEnabled {
		tlsDays = strings.Trim(strings.ReplaceAll(c.TLSWarningDays, ",", ", "), "[]") + " days before"
	}
	ipFamily := map[string]string{"": "Automatic", "ipv4": "IPv4 only", "ipv6": "IPv6 only"}[c.IPFamily]
	return append(gs,
		templates.ConfigGroup{Title: "Request", Facts: []templates.Fact{
			{Label: "URL", Value: c.URL},
			{Label: "Method", Value: c.Method},
			{Label: "Follow redirects", Value: yesNo(c.FollowRedirects, "Yes, up to 10", "No")},
			{Label: "Headers", Value: orNone(strings.Join(headers, "; "), "None")},
			{Label: "Authentication", Value: authLabel},
			{Label: "Secret headers", Value: orNone(strings.Join(secretHeaders, ", "), "None")},
		}},
		templates.ConfigGroup{Title: "Assertions", Facts: []templates.Fact{
			{Label: "Expected status", Value: c.ExpectedStatus},
			{Label: "Body contains", Value: orNone(c.BodyContains, "—")},
			{Label: "Body does not contain", Value: orNone(c.BodyNotContains, "—")},
			{Label: "JSON assertions", Value: orNone(strings.Join(assertions, "; "), "None")},
		}},
		templates.ConfigGroup{Title: "Advanced", Facts: []templates.Fact{
			{Label: "User-Agent", Value: orNone(c.CustomUserAgent, "Sinjal's own")},
			{Label: "Body limit", Value: strconv.Itoa(c.MaxBodyBytes/1024) + " KiB"},
			{Label: "Certificate warnings", Value: tlsDays},
			{Label: "Invalid certificates", Value: yesNo(c.InsecureSkipVerify, "Accepted (not recommended)", "Rejected")},
			{Label: "Proxy", Value: orNone(c.ProxyURL, "None")},
			{Label: "IP version", Value: ipFamily},
		}},
	), nil
}

// failureKinds are the readable names of the stored failure kinds.
var failureKinds = map[string]string{
	httpcheck.KindTimeout:       "Timeout",
	httpcheck.KindDNS:           "DNS lookup",
	httpcheck.KindConnect:       "Connection",
	httpcheck.KindTLS:           "TLS",
	httpcheck.KindHTTPStatus:    "HTTP status",
	httpcheck.KindBodyAssertion: "Body assertion",
	httpcheck.KindJSONAssertion: "JSON assertion",
	httpcheck.KindJSONParse:     "Invalid JSON",
	httpcheck.KindProtocol:      "Protocol",
	httpcheck.KindUnknown:       "Other",
}

// failureView formats one failed check. The message and the snippet are
// for admins only. The snippet is rendered as text by the template, so
// markup in a response body is shown, never interpreted.
func failureView(r store.CheckResult, admin bool) templates.FailureView {
	v := templates.FailureView{
		When:   r.CheckedAt.UTC().Format("2006-01-02 15:04:05") + " UTC",
		WhenAt: r.CheckedAt.UTC().Format(time.RFC3339),
		Kind:   failureKinds[r.ErrorKind],
		Status: r.ProtocolStatus,
	}
	if v.Kind == "" {
		v.Kind = "Other"
	}
	if r.Duration > 0 {
		v.Duration = formatLatency(r.Duration)
	}
	if admin {
		v.Message, v.Snippet = r.ErrorMessage, r.Snippet
	}
	return v
}

// pause serves POST /monitors/{id}/pause.
func (h *Monitors) pause(w http.ResponseWriter, r *http.Request) {
	h.setPaused(w, r, true)
}

// resume serves POST /monitors/{id}/resume.
func (h *Monitors) resume(w http.ResponseWriter, r *http.Request) {
	h.setPaused(w, r, false)
}

func (h *Monitors) setPaused(w http.ResponseWriter, r *http.Request, pause bool) {
	ctx, id := r.Context(), chi.URLParam(r, "id")
	m, err := store.GetMonitor(ctx, h.db.Reader, id)
	var changed bool
	if err == nil {
		if pause {
			changed, err = h.engine.Pause(ctx, id)
		} else {
			changed, err = h.engine.Resume(ctx, id)
		}
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, fmt.Sprintf("changing a monitor (pause=%v)", pause), err)
		return
	}
	if changed {
		typ := audit.MonitorResumed
		if pause {
			typ = audit.MonitorPaused
		}
		cs, _ := SessionFromContext(ctx)
		h.log.Info("monitor "+strings.TrimPrefix(typ, "monitor."), "user_id", cs.User.ID, "monitor_id", id)
		h.audit(r, typ, id, m.Name)
	}
	http.Redirect(w, r, "/monitors/"+id, http.StatusSeeOther)
}

// remove serves POST /monitors/{id}/delete. Without confirm=1 it only
// shows the confirmation page, so a stray post deletes nothing.
func (h *Monitors) remove(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), chi.URLParam(r, "id")
	m, err := store.GetMonitor(ctx, h.db.Reader, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "loading a monitor", err)
		return
	}
	if r.PostFormValue("confirm") != "1" {
		render(w, r, h.log, http.StatusOK, templates.MonitorDeleteConfirm(pageFor(r, "Delete "+m.Name+" — Sinjal"),
			templates.DeleteConfirmView{ID: id, Name: m.Name}))
		return
	}
	err = h.engine.Delete(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "deleting a monitor", err)
		return
	}
	cs, _ := SessionFromContext(ctx)
	h.log.Info("monitor deleted", "user_id", cs.User.ID, "monitor_id", id)
	h.audit(r, audit.MonitorDeleted, id, m.Name)
	h.events.Publish(sse.MonitorDeleted, id)
	http.Redirect(w, r, "/monitors", http.StatusSeeOther)
}
