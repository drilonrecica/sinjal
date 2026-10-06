package web

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/notify"
	"github.com/drilonrecica/sinjal/internal/store"
)

const testSecret = "TOPSECRET-VALUE"

func smtpForm(name string) url.Values {
	return url.Values{"type": {"smtp"}, "name": {name}, "enabled": {"1"}, "host": {"smtp.example.com"}, "port": {"587"},
		"security": {"starttls"}, "username": {"mailer"}, "password": {testSecret}, "from": {"sinjal@example.com"},
		"to": {"ops@example.com, dev@example.com"}}
}

func (e *appEnv) channelID(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := e.db.Reader.QueryRow(`SELECT id FROM notification_channels WHERE name = ?`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestChannelCreateEditDelete(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")

	if rec := e.getAs(t, "a1", "GET", "/notifications/channels/new"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="host"`) || !strings.Contains(rec.Body.String(), `value="587"`) {
		t.Fatalf("new form = %d", rec.Code)
	}
	rec := e.postAs(t, "a1", "/notifications/channels", smtpForm("Ops mail"))
	if rec.Code != 303 || rec.Header().Get("Location") != "/notifications" {
		t.Fatalf("create = %d %s\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	id := e.channelID(t, "Ops mail")
	c, cfg, err := store.GetChannel(t.Context(), e.db.Reader, e.key, id)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.(notify.SMTP)
	if c.Type != "smtp" || !c.Enabled || s.Host != "smtp.example.com" || s.Port != 587 || s.Password != testSecret || len(s.To) != 2 {
		t.Errorf("stored = %+v %#v", c, s)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'notification.channel_created' AND object_id = ? AND user_id = 'a1'`, id); n != 1 {
		t.Errorf("%d created audit events", n)
	}

	// The edit form shows the stored values, never the secret.
	rec = e.getAs(t, "a1", "GET", "/notifications/channels/"+id+"/edit")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `value="smtp.example.com"`) || !strings.Contains(body, "A value is saved") {
		t.Fatalf("edit form = %d", rec.Code)
	}
	if strings.Contains(body, testSecret) {
		t.Error("the edit form shows the secret")
	}
	if strings.Contains(body, `type="password" autocomplete="new-password" spellcheck="false" value=`) {
		t.Error("a secret input carries a value")
	}

	// Blank password keeps the stored one; the type posted is ignored.
	f := smtpForm("Renamed")
	f["password"], f["host"], f["type"] = []string{""}, []string{"mail2.example.com"}, []string{"telegram"}
	delete(f, "enabled")
	if rec := e.postAs(t, "a1", "/notifications/channels/"+id, f); rec.Code != 303 {
		t.Fatalf("update = %d\n%s", rec.Code, rec.Body)
	}
	c, cfg, _ = store.GetChannel(t.Context(), e.db.Reader, e.key, id)
	s = cfg.(notify.SMTP)
	if c.Name != "Renamed" || c.Type != "smtp" || c.Enabled || s.Host != "mail2.example.com" || s.Password != testSecret {
		t.Errorf("after update = %+v %#v", c, s)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'notification.channel_updated' AND object_id = ?`, id); n != 1 {
		t.Errorf("%d updated audit events", n)
	}

	// Delete asks first.
	rec = e.postAs(t, "a1", "/notifications/channels/"+id+"/delete", url.Values{})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Delete Renamed?") || e.count(t, `SELECT COUNT(*) FROM notification_channels`) != 1 {
		t.Fatalf("delete prompt = %d", rec.Code)
	}
	if rec := e.postAs(t, "a1", "/notifications/channels/"+id+"/delete", url.Values{"confirm": {"1"}}); rec.Code != 303 {
		t.Fatalf("delete = %d", rec.Code)
	}
	if e.count(t, `SELECT COUNT(*) FROM notification_channels`) != 0 {
		t.Error("not deleted")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'notification.channel_deleted' AND object_id = ?`, id); n != 1 {
		t.Errorf("%d deleted audit events", n)
	}
	for _, p := range []string{"/notifications/channels/" + id + "/edit"} {
		if rec := e.getAs(t, "a1", "GET", p); rec.Code != 404 {
			t.Errorf("GET %s after delete = %d", p, rec.Code)
		}
	}
	for _, p := range []string{"/notifications/channels/" + id, "/notifications/channels/" + id + "/delete"} {
		if rec := e.postAs(t, "a1", p, smtpForm("x")); rec.Code != 404 {
			t.Errorf("POST %s after delete = %d", p, rec.Code)
		}
	}
}

func TestChannelEveryType(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	forms := map[string]url.Values{
		"telegram": {"type": {"telegram"}, "name": {"tg"}, "enabled": {"1"}, "bot_token": {"123456:" + testSecret}, "chat_id": {"-1001"}},
		"discord":  {"type": {"discord"}, "name": {"dc"}, "enabled": {"1"}, "webhook_url": {"https://discord.com/api/webhooks/1/" + testSecret}},
		"webhook": {"type": {"webhook"}, "name": {"wh"}, "enabled": {"1"}, "url": {"https://hooks.example.com/in"},
			"header_name": {"Authorization"}, "header_value": {"Bearer " + testSecret}},
	}
	for typ, f := range forms {
		if rec := e.getAs(t, "a1", "GET", "/notifications/channels/new?type="+typ); rec.Code != 200 || !strings.Contains(rec.Body.String(), "aria-current") {
			t.Errorf("new %s form = %d", typ, rec.Code)
		}
		if rec := e.postAs(t, "a1", "/notifications/channels", f); rec.Code != 303 {
			t.Fatalf("create %s = %d\n%s", typ, rec.Code, rec.Body)
		}
		id := e.channelID(t, f.Get("name"))
		c, cfg, err := store.GetChannel(t.Context(), e.db.Reader, e.key, id)
		if err != nil || c.Type != typ || cfg.Type() != typ {
			t.Errorf("%s stored as %+v, %v", typ, c, err)
		}
		if body := e.getAs(t, "a1", "GET", "/notifications/channels/"+id+"/edit").Body.String(); strings.Contains(body, testSecret) {
			t.Errorf("%s edit form shows the secret", typ)
		}
	}
	// An unknown type falls back to the first on the form and is refused on post.
	if rec := e.getAs(t, "a1", "GET", "/notifications/channels/new?type=sms"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="host"`) {
		t.Errorf("unknown type form = %d", rec.Code)
	}
	if rec := e.postAs(t, "a1", "/notifications/channels", url.Values{"type": {"sms"}, "name": {"x"}}); rec.Code != 400 {
		t.Errorf("unknown type post = %d", rec.Code)
	}
}

func TestChannelFormErrorsAreAllShownAndNothingStored(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	f := smtpForm("")
	f["host"], f["port"], f["from"], f["password"] = []string{""}, []string{"abc"}, []string{"nope"}, []string{testSecret}
	rec := e.postAs(t, "a1", "/notifications/channels", f)
	body := rec.Body.String()
	if rec.Code != 422 {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{"Fix 4 problems to save", "#name", "#host", "#port", "#from", `aria-invalid="true"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(body, testSecret) {
		t.Error("a rejected form shows the secret back")
	}
	if !strings.Contains(body, `value="abc"`) && !strings.Contains(body, `value="mailer"`) {
		t.Error("the rejected values are not shown back")
	}
	if e.count(t, `SELECT COUNT(*) FROM notification_channels`) != 0 || e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type LIKE 'notification.%'`) != 0 {
		t.Error("a rejected form wrote something")
	}

	// A rejected edit still says the secret is stored.
	id, err := store.CreateChannel(t.Context(), e.db, e.key, store.ChannelInput{Name: "ok", Enabled: true,
		Config: notify.FromValues("smtp", func(k string) string { return smtpForm("ok").Get(k) })}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f = smtpForm("ok")
	f["port"], f["password"] = []string{"0"}, []string{""}
	rec = e.postAs(t, "a1", "/notifications/channels/"+id, f)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "A value is saved") {
		t.Errorf("rejected edit = %d, saved note: %v", rec.Code, strings.Contains(rec.Body.String(), "A value is saved"))
	}
}

func TestNotificationsListAndPermissions(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")

	// Empty state.
	if body := e.getAs(t, "a1", "GET", "/notifications").Body.String(); !strings.Contains(body, "No channels yet") || !strings.Contains(body, "Add channel") {
		t.Error("empty state missing")
	}
	e.postAs(t, "a1", "/notifications/channels", smtpForm("Ops mail"))
	id := e.channelID(t, "Ops mail")
	e.db.Writer.Exec(`UPDATE notification_channels SET health_state = 'failed', last_failure_at = '2026-10-06T10:00:00Z', last_error = 'connection refused' WHERE id = ?`, id)

	admin := e.getAs(t, "a1", "GET", "/notifications").Body.String()
	for _, want := range []string{"Ops mail", "Email (SMTP)", "Failed", "Last failure", "connection refused", "/notifications/channels/" + id + "/edit"} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin list lacks %q", want)
		}
	}
	if strings.Contains(admin, testSecret) || strings.Contains(admin, "smtp.example.com") {
		t.Error("the list shows configuration")
	}

	// A viewer sees the channels but can neither open nor change one.
	rec := e.getAs(t, "v1", "GET", "/notifications")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Ops mail") || strings.Contains(rec.Body.String(), "/edit") || strings.Contains(rec.Body.String(), "Add channel") {
		t.Errorf("viewer list = %d", rec.Code)
	}
	for _, p := range []string{"/notifications/channels/new", "/notifications/channels/" + id + "/edit"} {
		if rec := e.getAs(t, "v1", "GET", p); rec.Code != 403 {
			t.Errorf("viewer GET %s = %d, want 403", p, rec.Code)
		}
	}
	for _, p := range []string{"/notifications/channels", "/notifications/channels/" + id, "/notifications/channels/" + id + "/delete"} {
		if rec := e.postAs(t, "v1", p, smtpForm("hacked")); rec.Code != 403 {
			t.Errorf("viewer POST %s = %d, want 403", p, rec.Code)
		}
	}
	if e.count(t, `SELECT COUNT(*) FROM notification_channels WHERE name = 'hacked'`) != 0 {
		t.Error("a viewer changed a channel")
	}

	// Disabled is shown in words.
	e.db.Writer.Exec(`UPDATE notification_channels SET enabled = 0 WHERE id = ?`, id)
	if body := e.getAs(t, "a1", "GET", "/notifications").Body.String(); !strings.Contains(body, "Disabled") {
		t.Error("a disabled channel is not marked")
	}
}

func TestChannelSecretsNeverReachTheLog(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.postAs(t, "a1", "/notifications/channels", smtpForm("Ops mail"))
	bad := smtpForm("bad")
	bad["port"] = []string{"0"}
	e.postAs(t, "a1", "/notifications/channels", bad)
	if strings.Contains(e.logs.String(), testSecret) {
		t.Error("a secret was logged")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE metadata_json LIKE ?`, "%"+testSecret+"%"); n != 0 {
		t.Error("a secret was audited")
	}
}
