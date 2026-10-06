package web

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/audit"
)

func TestSettingsSystemShowsAuditLog(t *testing.T) {
	e := newTOTPEnv(t)

	// Real actions write the events: a failed and a successful sign-in.
	e.serve(req("POST", "/login", url.Values{"login": {"admin"}, "password": {"not the password"}}))
	e.serve(req("POST", "/login", url.Values{"login": {"admin"}, "password": {testPassword}}))

	rec := e.get("/settings/system")
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("GET = %d", rec.Code)
	}
	for _, want := range []string{"<h1>System</h1>", "Audit log", "auth.login_failed", "auth.login_succeeded", "client_ip=", "admin"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, secret := range []string{testPassword, "not the password"} {
		if strings.Contains(body, secret) {
			t.Errorf("the audit page shows %q", secret)
		}
	}
	if strings.Index(body, "auth.login_succeeded") > strings.Index(body, "auth.login_failed") {
		t.Error("the newest event is not first")
	}
}

func TestSettingsSystemPaging(t *testing.T) {
	e := newTOTPEnv(t)
	for i := range audit.PageSize + 5 {
		if err := audit.Record(t.Context(), e.db, audit.Event{Type: audit.LoginFailed, ObjectID: fmt.Sprintf("n%03d", i)}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	body := e.get("/settings/system").Body.String()
	m := regexpFind(t, `href="(/settings/system\?before=\d+)"`, body)
	if !strings.Contains(body, fmt.Sprintf("n%03d", audit.PageSize+4)) || strings.Contains(body, "n004") {
		t.Error("the first page is not the newest 50")
	}
	older := e.get(strings.ReplaceAll(m, "&amp;", "&")).Body.String()
	if !strings.Contains(older, "n004") || strings.Contains(older, "Older events") {
		t.Error("the second page lacks the oldest events or still offers older ones")
	}
	// A bad cursor is the first page, not an error.
	for _, q := range []string{"?before=abc", "?before=-5", "?before=0"} {
		if rec := e.get("/settings/system" + q); rec.Code != 200 {
			t.Errorf("%s = %d", q, rec.Code)
		}
	}
}

func TestSettingsSystemIsAdminOnly(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "v1", "vera", "viewer", viewerPassword)
	cookie, _ := e.signIn(t, "v1")
	if rec := e.serve(withCookie(req("GET", "/settings/system", nil), cookie)); rec.Code != 403 {
		t.Errorf("a viewer reading the audit log = %d, want 403", rec.Code)
	}
	if rec := e.serve(req("GET", "/settings/system", nil)); rec.Code != 303 {
		t.Errorf("anonymous = %d, want 303 to /login", rec.Code)
	}
}

func regexpFind(t *testing.T, pattern, s string) string {
	t.Helper()
	return match(t, regexp.MustCompile(pattern), s)
}
