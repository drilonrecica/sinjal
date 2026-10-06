package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/notify"
	"github.com/drilonrecica/sinjal/internal/store"
)

// hook is a fake webhook endpoint that records the payloads it gets and
// answers with status.
type hook struct {
	*httptest.Server
	mu       sync.Mutex
	payloads []map[string]any
	headers  []http.Header
	status   int
}

func newHook(t *testing.T, status int) *hook {
	t.Helper()
	h := &hook{status: status}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p map[string]any
		_ = json.Unmarshal(b, &p)
		h.mu.Lock()
		h.payloads, h.headers = append(h.payloads, p), append(h.headers, r.Header.Clone())
		h.mu.Unlock()
		w.WriteHeader(h.status)
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *hook) got() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.payloads...)
}

func (e *appEnv) addHookChannel(t *testing.T, name, url string, enabled bool) string {
	t.Helper()
	id, err := store.CreateChannel(context.Background(), e.db, e.key, store.ChannelInput{Name: name, Enabled: enabled,
		Config: notify.Webhook{URL: url, HeaderName: "X-Token", HeaderValue: testSecret}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *appEnv) profileID(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := e.db.Reader.QueryRow(`SELECT id FROM notification_profiles WHERE name = ?`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestProfileCreateEditDelete(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	mail := e.addHookChannel(t, "Mail", "http://127.0.0.1:1/mail", true)
	chat := e.addHookChannel(t, "Chat", "http://127.0.0.1:1/chat", false)

	rec := e.getAs(t, "a1", "GET", "/notifications/profiles/new")
	body := rec.Body.String()
	for _, want := range []string{`value="critical:` + mail + `"`, `class="visually-hidden">Critical to Mail</label>`, "Chat", ", disabled", `name="critical_bypass" type="checkbox" value="1" checked`, "In UTC."} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Errorf("new form (%d) lacks %q", rec.Code, want)
		}
	}

	rec = e.postAs(t, "a1", "/notifications/profiles", url.Values{"name": {" Critical "}, "route": {"critical:" + mail, "critical:" + chat, "info:" + chat},
		"quiet_enabled": {"1"}, "quiet_start": {"23:00"}, "quiet_end": {"07:00"}, "critical_bypass": {"1"}, "reminder": {"60"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/notifications" {
		t.Fatalf("create = %d\n%s", rec.Code, rec.Body)
	}
	id := e.profileID(t, "Critical")
	p, err := store.GetProfile(t.Context(), e.db.Reader, id)
	if err != nil || !p.QuietEnabled || p.QuietStart != "23:00" || !p.CriticalBypass || p.ReminderAfter != time.Hour ||
		len(p.Routes["critical"]) != 2 || len(p.Routes["info"]) != 1 || len(p.Routes["warning"]) != 0 {
		t.Fatalf("stored = %+v, %v", p, err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'notification.profile_created' AND object_id = ? AND user_id = 'a1'`, id); n != 1 {
		t.Errorf("%d created audit events", n)
	}

	body = e.getAs(t, "a1", "GET", "/notifications").Body.String()
	for _, want := range []string{"Critical: Chat, Mail", "Info: Chat", "Quiet 23:00–07:00 UTC, critical bypasses", "Reminder after 1h 00m", "Used by no monitor"} {
		if !strings.Contains(body, want) {
			t.Errorf("list lacks %q", want)
		}
	}

	body = e.getAs(t, "a1", "GET", "/notifications/profiles/"+id+"/edit").Body.String()
	for _, want := range []string{`value="critical:` + mail + `" checked`, `value="info:` + chat + `" checked`, `value="23:00"`, `value="60"`, "Simulate incident"} {
		if !strings.Contains(body, want) {
			t.Errorf("edit form lacks %q", want)
		}
	}
	if strings.Contains(body, `value="warning:`+mail+`" checked`) {
		t.Error("an unchosen route is checked")
	}

	// Update: everything off, one route.
	if rec := e.postAs(t, "a1", "/notifications/profiles/"+id, url.Values{"name": {"Quiet"}, "route": {"warning:" + mail}}); rec.Code != 303 {
		t.Fatalf("update = %d\n%s", rec.Code, rec.Body)
	}
	p, _ = store.GetProfile(t.Context(), e.db.Reader, id)
	if p.Name != "Quiet" || p.QuietEnabled || p.CriticalBypass || p.ReminderAfter != 0 || len(p.Routes) != 1 {
		t.Errorf("after update = %+v", p)
	}

	// A monitor uses it: the prompt says so, and the monitor stays.
	e.addMonitor(t, "API", "https://example.com")
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET notification_profile_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	rec = e.postAs(t, "a1", "/notifications/profiles/"+id+"/delete", url.Values{})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "1 monitor uses this profile") {
		t.Fatalf("delete prompt = %d\n%s", rec.Code, rec.Body)
	}
	if rec := e.postAs(t, "a1", "/notifications/profiles/"+id+"/delete", url.Values{"confirm": {"1"}}); rec.Code != 303 {
		t.Fatalf("delete = %d", rec.Code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM monitors WHERE notification_profile_id IS NULL`); n != 1 {
		t.Errorf("%d monitors without a profile", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type IN ('notification.profile_updated', 'notification.profile_deleted')`); n != 2 {
		t.Errorf("%d update/delete audit events", n)
	}
	if rec := e.getAs(t, "a1", "GET", "/notifications/profiles/"+id+"/edit"); rec.Code != 404 {
		t.Errorf("edit of a deleted profile = %d", rec.Code)
	}
}

func TestProfileFormErrors(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addHookChannel(t, "Mail", "http://127.0.0.1:1/mail", true)
	if rec := e.postAs(t, "a1", "/notifications/profiles", url.Values{"name": {"Ops"}}); rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	rec := e.postAs(t, "a1", "/notifications/profiles", url.Values{"name": {"Ops"}, "quiet_enabled": {"1"}, "quiet_start": {"9"},
		"reminder": {"soon"}, "route": {"critical:gone"}})
	body := rec.Body.String()
	if rec.Code != 422 {
		t.Fatalf("status %d", rec.Code)
	}
	for _, want := range []string{"Fix 5 problems to save", "Another profile has this name.", "Enter a time such as 23:00.", "Enter a time such as 07:00.",
		"Use whole minutes from 1 to 10080", "A chosen channel no longer exists.", `value="soon"`, `value="9"`} {
		if !strings.Contains(body, want) {
			t.Errorf("error page lacks %q", want)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM notification_profiles`); n != 1 {
		t.Errorf("%d profiles", n)
	}
	if rec := e.postAs(t, "a1", "/notifications/profiles", url.Values{"name": {"x"}, "route": {"nocolon"}}); rec.Code != 400 {
		t.Errorf("malformed route = %d", rec.Code)
	}
}

// The list's empty states: no channel, then a channel but no profile.
func TestNotificationsEmptyStates(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	body := e.getAs(t, "a1", "GET", "/notifications").Body.String()
	if !strings.Contains(body, "No channels yet") || !strings.Contains(body, "Add a channel first") || strings.Contains(body, `href="/notifications/profiles/new"`) {
		t.Errorf("no channels:\n%s", body)
	}
	e.addHookChannel(t, "Mail", "http://127.0.0.1:1/mail", true)
	body = e.getAs(t, "a1", "GET", "/notifications").Body.String()
	if !strings.Contains(body, "No profiles yet") || !strings.Contains(body, `href="/notifications/profiles/new"`) {
		t.Errorf("no profiles:\n%s", body)
	}
	body = e.getAs(t, "v1", "GET", "/notifications").Body.String()
	if strings.Contains(body, "/notifications/profiles/new") || strings.Contains(body, "/edit") {
		t.Error("a viewer gets change links")
	}
	// The live fragment is the channel section, viewers included.
	rec := e.getAs(t, "v1", "GET", "/fragments/notifications")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "data-live-list") || !strings.Contains(rec.Body.String(), "Mail") {
		t.Errorf("fragment = %d\n%s", rec.Code, rec.Body)
	}
}

func TestViewerCannotChangeNotifications(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "viewer", "viewer", "")
	ch := e.addHookChannel(t, "Mail", "http://127.0.0.1:1/mail", true)
	p, err := store.CreateProfile(t.Context(), e.db, store.ProfileInput{Name: "Ops"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/notifications/channels/" + ch + "/test", "/notifications/profiles", "/notifications/profiles/" + p,
		"/notifications/profiles/" + p + "/simulate", "/notifications/profiles/" + p + "/delete"} {
		if rec := e.postAs(t, "v1", path, url.Values{"name": {"x"}, "confirm": {"1"}}); rec.Code != 403 {
			t.Errorf("POST %s as viewer = %d", path, rec.Code)
		}
	}
	for _, path := range []string{"/notifications/profiles/new", "/notifications/profiles/" + p + "/edit"} {
		if rec := e.getAs(t, "v1", "GET", path); rec.Code != 403 {
			t.Errorf("GET %s as viewer = %d", path, rec.Code)
		}
	}
	if e.count(t, `SELECT COUNT(*) FROM notification_deliveries`)+e.count(t, `SELECT COUNT(*) FROM notification_profiles`) != 1 {
		t.Error("a viewer changed something")
	}
}

// "Send test notification" sends a [TEST] DOWN through the saved
// configuration, records it like a delivery and shows the outcome; the
// channel's secret appears nowhere.
func TestChannelSendTest(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	stream := e.subscribe(t)
	good := newHook(t, 204)
	id := e.addHookChannel(t, "Hook", good.URL+"/in", false) // disabled channels can be tested

	rec := e.postAs(t, "a1", "/notifications/channels/"+id+"/test", url.Values{})
	if rec.Code != 303 || rec.Header().Get("Location") != "/notifications/channels/"+id+"/edit?test=sent" {
		t.Fatalf("test = %d %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	// The page after the redirect shows the outcome; loading it sends nothing.
	if body := e.getAs(t, "a1", "GET", rec.Header().Get("Location")).Body.String(); !strings.Contains(body, "Test notification sent to Hook") {
		t.Fatalf("result page:\n%s", body)
	}
	got := good.got()
	if len(got) != 1 || got[0]["event"] != "monitor.down" || got[0]["test"] != true || good.headers[0].Get("X-Token") != testSecret {
		t.Fatalf("endpoint got %v", got)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM notification_deliveries WHERE channel_id = ? AND event_type = 'test' AND status = 'sent' AND incident_id IS NULL AND delivered_at IS NOT NULL`, id); n != 1 {
		t.Errorf("%d test delivery rows", n)
	}
	if c, _ := store.GetChannelInfo(t.Context(), e.db.Reader, id); c.HealthState != store.HealthHealthy {
		t.Errorf("health %s", c.HealthState)
	}
	waitUntil(t, "the channel event", func() bool { return strings.Contains(stream(), "notification.channel_updated") })

	bad := newHook(t, 500)
	failing := e.addHookChannel(t, "Broken", bad.URL, true)
	rec = e.postAs(t, "a1", "/notifications/channels/"+failing+"/test", url.Values{})
	if rec.Code != 303 || !strings.HasSuffix(rec.Header().Get("Location"), "?test=failed") {
		t.Fatalf("failing test = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	body := e.getAs(t, "a1", "GET", rec.Header().Get("Location")).Body.String()
	if !strings.Contains(body, "The test notification failed: webhook: 500 Internal Server Error") {
		t.Fatalf("failing result page:\n%s", body)
	}
	if n := len(bad.got()) + len(good.got()); n != 2 {
		t.Errorf("%d sends; reloading a result page must send nothing", n)
	}
	if c, _ := store.GetChannelInfo(t.Context(), e.db.Reader, failing); c.HealthState != store.HealthFailed || c.LastError != "webhook: 500 Internal Server Error" {
		t.Errorf("health %+v", c)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'notification.channel_tested'`); n != 2 {
		t.Errorf("%d tested audit events", n)
	}
	for what, s := range map[string]string{"page": body, "log": e.logs.String()} {
		if strings.Contains(s, testSecret) {
			t.Errorf("the %s shows the secret", what)
		}
	}
	if rec := e.postAs(t, "a1", "/notifications/channels/nope/test", url.Values{}); rec.Code != 404 {
		t.Errorf("unknown channel = %d", rec.Code)
	}
}

// "Simulate incident" sends [TEST] messages along the profile's critical
// and info routes and leaves no trace in the history.
func TestSimulateIncident(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	crit, info, off := newHook(t, 204), newHook(t, 204), newHook(t, 204)
	failing := newHook(t, 503)
	c1 := e.addHookChannel(t, "Pager", crit.URL, true)
	c2 := e.addHookChannel(t, "Chat", info.URL, true)
	c3 := e.addHookChannel(t, "Off", off.URL, false)
	c4 := e.addHookChannel(t, "Down", failing.URL, true)
	p, err := store.CreateProfile(t.Context(), e.db, store.ProfileInput{Name: "Ops", CriticalBypass: true,
		QuietEnabled: true, QuietStart: "00:00", QuietEnd: "23:59",
		Routes: map[string][]string{"critical": {c1, c3, c4}, "info": {c2}, "warning": {c2}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	before := e.count(t, `SELECT (SELECT COUNT(*) FROM incidents) + (SELECT COUNT(*) FROM incident_events) + (SELECT COUNT(*) FROM notification_deliveries) + (SELECT COUNT(*) FROM check_results)`)

	rec := e.postAs(t, "a1", "/notifications/profiles/"+p+"/simulate", url.Values{})
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("simulate = %d\n%s", rec.Code, body)
	}
	for _, want := range []string{"Simulated incident: Ops", "Pager", "[TEST] DOWN", "Chat", "[TEST] RECOVERY", "webhook: 503 Service Unavailable",
		"a real DOWN would be sent, because critical notifications bypass them"} {
		if !strings.Contains(body, want) {
			t.Errorf("result page lacks %q", want)
		}
	}
	if g := crit.got(); len(g) != 1 || g[0]["event"] != "monitor.down" || g[0]["test"] != true {
		t.Errorf("critical channel got %v", g)
	}
	if g := info.got(); len(g) != 1 || g[0]["event"] != "monitor.recovered" || g[0]["test"] != true {
		t.Errorf("info channel got %v", g)
	}
	if g := off.got(); len(g) != 0 {
		t.Errorf("disabled channel got %v", g)
	}
	after := e.count(t, `SELECT (SELECT COUNT(*) FROM incidents) + (SELECT COUNT(*) FROM incident_events) + (SELECT COUNT(*) FROM notification_deliveries) + (SELECT COUNT(*) FROM check_results)`)
	if after != before {
		t.Errorf("history rows %d → %d", before, after)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM notification_channels WHERE health_state <> 'unknown' OR last_error IS NOT NULL`); n != 0 {
		t.Errorf("%d channels changed health", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'notification.profile_simulated' AND object_id = ?`, p); n != 1 {
		t.Errorf("%d simulated audit events", n)
	}

	// A profile that routes nothing says so.
	empty, _ := store.CreateProfile(t.Context(), e.db, store.ProfileInput{Name: "Empty"}, time.Now())
	if body := e.postAs(t, "a1", "/notifications/profiles/"+empty+"/simulate", url.Values{}).Body.String(); !strings.Contains(body, "Nothing was sent") {
		t.Errorf("empty profile:\n%s", body)
	}
}

// The monitor form offers the profiles; the choice is saved, kept on edit
// and an unknown one is refused.
func TestMonitorFormProfilePicker(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tg.Close()

	if body := e.getAs(t, "a1", "GET", "/monitors/new").Body.String(); !strings.Contains(body, "There are no profiles yet") {
		t.Error("no hint without profiles")
	}
	p, err := store.CreateProfile(t.Context(), e.db, store.ProfileInput{Name: "Ops"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if body := e.getAs(t, "a1", "GET", "/monitors/new").Body.String(); !strings.Contains(body, `<option value="`+p+`">Ops</option>`) {
		t.Error("the profile is not offered")
	}
	rec := e.postAs(t, "a1", "/monitors", url.Values{"name": {"API"}, "url": {tg.URL}, "enabled": {"1"}, "notification_profile_id": {p}})
	if rec.Code != 303 {
		t.Fatalf("create = %d\n%s", rec.Code, rec.Body)
	}
	id := e.monitorID(t, "API")
	m, _ := store.GetMonitor(t.Context(), e.db.Reader, id)
	if m.NotificationProfileID != p {
		t.Fatalf("profile = %q", m.NotificationProfileID)
	}
	if body := e.getAs(t, "a1", "GET", "/monitors/"+id+"/edit").Body.String(); !strings.Contains(body, `<option value="`+p+`" selected>Ops</option>`) {
		t.Error("the edit form does not show the profile")
	}
	if body := e.getAs(t, "a1", "GET", "/monitors/"+id+"?tab=configuration").Body.String(); !strings.Contains(body, "Ops") {
		t.Error("the detail page does not name the profile")
	}

	rec = e.postAs(t, "a1", "/monitors/"+id, url.Values{"name": {"API"}, "url": {tg.URL}, "enabled": {"1"}, "notification_profile_id": {"nope"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "The notification profile does not exist.") ||
		!strings.Contains(rec.Body.String(), `href="#notification_profile_id"`) {
		t.Fatalf("unknown profile = %d\n%s", rec.Code, rec.Body)
	}
	// Clearing it on edit.
	if rec := e.postAs(t, "a1", "/monitors/"+id, url.Values{"name": {"API"}, "url": {tg.URL}, "enabled": {"1"}}); rec.Code != 303 {
		t.Fatalf("update = %d\n%s", rec.Code, rec.Body)
	}
	if m, _ := store.GetMonitor(t.Context(), e.db.Reader, id); m.NotificationProfileID != "" {
		t.Errorf("profile after clearing = %q", m.NotificationProfileID)
	}
}
