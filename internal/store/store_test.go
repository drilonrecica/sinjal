package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/vault"
)

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func testDB(t testing.TB) *db.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "backups"), "test", quiet); err != nil {
		t.Fatal(err)
	}
	return d
}

func testKey(t *testing.T, d *db.DB) *vault.Key {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	k, err := vault.LoadOrCreate(context.Background(), t.TempDir(), d.Reader, quiet)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func count(t *testing.T, d *db.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Reader.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sample(name string) MonitorInput {
	return MonitorInput{Name: name, Enabled: true, HTTP: HTTPConfig{URL: "https://example.com/" + name, FollowRedirects: true, TLSExpiryEnabled: true}}
}

func TestCreateMonitorAppliesDefaults(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	in := sample("web")
	in.Tags = []string{"prod", "edge"}
	id, err := CreateMonitor(ctx, d, in, now)
	if err != nil {
		t.Fatal(err)
	}
	m, err := GetMonitor(ctx, d.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "web" || m.Type != "http" || !m.Enabled || m.State != "pending" || !m.StateSince.Equal(now) {
		t.Errorf("monitor = %+v", m)
	}
	if m.IntervalSeconds != 30 || m.TimeoutMS != 5000 || m.FailureThreshold != 2 || m.RetryDelayMS != 5000 || m.SuccessThreshold != 1 {
		t.Errorf("defaults not applied: %+v", m)
	}
	if m.ParentMonitorID != "" || m.NotificationProfileID != "" || m.LastCheckAt != nil || m.FlappingSince != nil {
		t.Errorf("nullable fields = %+v", m)
	}
	c, err := GetHTTPConfig(ctx, d.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Method != "GET" || c.ExpectedStatus != "200-399" || c.MaxBodyBytes != 1048576 || c.TLSWarningDays != "[30,14,7]" || !c.FollowRedirects || c.Headers != "" {
		t.Errorf("config = %+v", c)
	}
	tags, _ := MonitorTags(ctx, d.Reader, id)
	if !reflect.DeepEqual(tags, []string{"edge", "prod"}) {
		t.Errorf("tags = %v", tags)
	}
}

func TestCreateDisabledMonitorIsPaused(t *testing.T) {
	d := testDB(t)
	in := sample("off")
	in.Enabled = false
	id, err := CreateMonitor(context.Background(), d, in, now)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := GetMonitor(context.Background(), d.Reader, id)
	if m.Enabled || m.State != "paused" {
		t.Errorf("enabled=%v state=%s", m.Enabled, m.State)
	}
}

func TestCreateValidatesAndWritesNothingOnFailure(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	var fe FieldErrors
	for name, c := range map[string]struct {
		mod   func(*MonitorInput)
		field string
	}{
		"no name":      {func(m *MonitorInput) { m.Name = "  " }, "name"},
		"long name":    {func(m *MonitorInput) { m.Name = strings.Repeat("x", 101) }, "name"},
		"no url":       {func(m *MonitorInput) { m.HTTP.URL = "" }, "url"},
		"empty tag":    {func(m *MonitorInput) { m.Tags = []string{"ok", " "} }, "tags"},
		"comma in tag": {func(m *MonitorInput) { m.Tags = []string{"a,b"} }, "tags"},
	} {
		in := sample("x")
		c.mod(&in)
		if _, err := CreateMonitor(ctx, d, in, now); !errors.As(err, &fe) || fe[c.field] == "" {
			t.Errorf("%s: error = %v, want a %s field error", name, err, c.field)
		}
	}
	if n := count(t, d, `SELECT (SELECT COUNT(*) FROM monitors) + (SELECT COUNT(*) FROM http_monitor_config) + (SELECT COUNT(*) FROM tags)`); n != 0 {
		t.Errorf("failed creations left %d rows (the bad-tag case must roll back the monitor)", n)
	}
}

func TestGetMonitorNotFound(t *testing.T) {
	d := testDB(t)
	if _, err := GetMonitor(context.Background(), d.Reader, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMonitor = %v", err)
	}
	if _, err := GetHTTPConfig(context.Background(), d.Reader, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetHTTPConfig = %v", err)
	}
}

func TestUpdateMonitor(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	in := sample("web")
	in.Tags = []string{"a", "b"}
	id, _ := CreateMonitor(ctx, d, in, now)
	if _, err := d.Writer.Exec(`UPDATE monitors SET current_state='up', last_check_at=? WHERE id=?`, formatTime(now), id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer.Exec(`INSERT INTO check_results (monitor_id, checked_at, success) VALUES (?, ?, 1)`, id, formatTime(now)); err != nil {
		t.Fatal(err)
	}

	later := now.Add(time.Hour)
	up := sample("web2")
	up.IntervalSeconds, up.HTTP.ExpectedStatus, up.HTTP.Headers = 60, "200", `[{"name":"X","value":"y"}]`
	up.Tags = []string{"B", "c"} // "B" is the existing "b"
	if err := UpdateMonitor(ctx, d, id, up, later); err != nil {
		t.Fatal(err)
	}
	m, _ := GetMonitor(ctx, d.Reader, id)
	if m.Name != "web2" || m.IntervalSeconds != 60 || !m.UpdatedAt.Equal(later) || !m.CreatedAt.Equal(now) {
		t.Errorf("monitor = %+v", m)
	}
	if m.State != "up" || m.LastCheckAt == nil {
		t.Error("an edit must not reset state or check history")
	}
	if n := count(t, d, `SELECT COUNT(*) FROM check_results WHERE monitor_id = ?`, id); n != 1 {
		t.Error("an edit must keep check results")
	}
	c, _ := GetHTTPConfig(ctx, d.Reader, id)
	if c.ExpectedStatus != "200" || c.Headers != `[{"name":"X","value":"y"}]` {
		t.Errorf("config = %+v", c)
	}
	tags, _ := ListTags(ctx, d.Reader)
	if !reflect.DeepEqual(tags, []string{"b", "c"}) {
		t.Errorf("tags = %v, want unused tag a gone and b reused case-insensitively", tags)
	}
	if err := UpdateMonitor(ctx, d, "nope", up, later); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of a missing monitor = %v", err)
	}
}

func TestDeleteMonitorCascades(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	in := sample("web")
	in.Tags = []string{"t"}
	id, _ := CreateMonitor(ctx, d, in, now)
	if err := SetSecret(ctx, d, k, id, "auth.bearer", []byte("s3cret"), now); err != nil {
		t.Fatal(err)
	}
	if err := DeleteMonitor(ctx, d, id); err != nil {
		t.Fatal(err)
	}
	for _, tb := range []string{"monitors", "http_monitor_config", "monitor_secrets", "monitor_tags", "tags"} {
		if n := count(t, d, `SELECT COUNT(*) FROM `+tb); n != 0 {
			t.Errorf("%s has %d rows after delete", tb, n)
		}
	}
	if err := DeleteMonitor(ctx, d, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v", err)
	}
}

// docs/08: listing must not pull secret blobs. Dropping the table proves no
// read path of the list touches it.
func TestListAndGetNeverTouchSecrets(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	for _, n := range []string{"b", "A", "c"} {
		id, err := CreateMonitor(ctx, d, sample(n), now)
		if err != nil {
			t.Fatal(err)
		}
		if err := SetSecret(ctx, d, k, id, "auth.bearer", []byte("s3cret"), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Writer.Exec(`DROP TABLE monitor_secrets`); err != nil {
		t.Fatal(err)
	}
	list, err := ListMonitors(ctx, d.Reader)
	if err != nil {
		t.Fatalf("ListMonitors must not read monitor_secrets: %v", err)
	}
	if len(list) != 3 || list[0].Name != "A" || list[1].Name != "b" || list[2].Name != "c" {
		t.Errorf("list = %+v, want case-insensitive name order", list)
	}
	if _, err := GetMonitor(ctx, d.Reader, list[0].ID); err != nil {
		t.Errorf("GetMonitor must not read monitor_secrets: %v", err)
	}
	if _, err := GetHTTPConfig(ctx, d.Reader, list[0].ID); err != nil {
		t.Errorf("GetHTTPConfig must not read monitor_secrets: %v", err)
	}
}

func TestTags(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	a, _ := CreateMonitor(ctx, d, sample("a"), now)
	b, _ := CreateMonitor(ctx, d, sample("b"), now)
	if err := SetMonitorTags(ctx, d, a, []string{"prod", "Prod", " web "}); err != nil {
		t.Fatal(err)
	}
	if err := SetMonitorTags(ctx, d, b, []string{"PROD"}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM tags`); n != 2 {
		t.Errorf("tags = %d, want 2 (prod shared, web)", n)
	}
	by, err := TagsByMonitor(ctx, d.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(by[a], []string{"prod", "web"}) || !reflect.DeepEqual(by[b], []string{"prod"}) {
		t.Errorf("by monitor = %v", by)
	}
	if err := SetMonitorTags(ctx, d, a, nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM tags`); n != 1 {
		t.Errorf("tags = %d, want 1 (web is unused and removed)", n)
	}
	if err := SetMonitorTags(ctx, d, "nope", []string{"x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("tags of a missing monitor = %v", err)
	}
}

func TestSecrets(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	a, _ := CreateMonitor(ctx, d, sample("a"), now)
	b, _ := CreateMonitor(ctx, d, sample("b"), now)

	if err := SetSecret(ctx, d, k, a, "auth.bearer", []byte("hunter2"), now); err != nil {
		t.Fatal(err)
	}
	if err := SetSecret(ctx, d, k, a, "auth.bearer", []byte("hunter3"), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := GetSecret(ctx, d.Reader, k, a, "auth.bearer")
	if err != nil || string(got) != "hunter3" {
		t.Fatalf("GetSecret = %q, %v", got, err)
	}
	var raw []byte
	if err := d.Reader.QueryRow(`SELECT value_enc FROM monitor_secrets WHERE monitor_id = ?`, a).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter") || raw[0] != 1 {
		t.Error("the secret must be stored as an encrypted v1 envelope")
	}
	names, _ := SecretNames(ctx, d.Reader, a)
	if !reflect.DeepEqual(names, []string{"auth.bearer"}) {
		t.Errorf("names = %v", names)
	}

	// An envelope copied to another monitor or name must not open.
	if _, err := d.Writer.Exec(`INSERT INTO monitor_secrets (monitor_id, key, value_enc, updated_at) VALUES (?, 'auth.bearer', ?, ?)`, b, raw, formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := GetSecret(ctx, d.Reader, k, b, "auth.bearer"); !errors.Is(err, vault.ErrDecrypt) {
		t.Errorf("a copied envelope opened: %v", err)
	}
	if _, err := GetSecret(ctx, d.Reader, k, a, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing secret = %v", err)
	}
	if err := SetSecret(ctx, d, k, a, "", []byte("x"), now); err == nil {
		t.Error("an empty secret name must be rejected")
	}
	if err := DeleteSecret(ctx, d, a, "auth.bearer"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteSecret(ctx, d, a, "auth.bearer"); err != nil {
		t.Errorf("deleting a missing secret = %v", err)
	}
	if _, err := GetSecret(ctx, d.Reader, k, a, "auth.bearer"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted secret = %v", err)
	}
}

func TestSecretsDecryptsAllForExecutor(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	id, _ := CreateMonitor(ctx, d, sample("a"), now)
	other, _ := CreateMonitor(ctx, d, sample("b"), now)
	_ = SetSecret(ctx, d, k, id, "auth.basic", []byte("u:p"), now)
	_ = SetSecret(ctx, d, k, id, "header.X-Api-Key", []byte("k1"), now)
	_ = SetSecret(ctx, d, k, other, "auth.bearer", []byte("not mine"), now)
	got, err := Secrets(ctx, d.Reader, k, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["auth.basic"].Reveal() != "u:p" || got["header.X-Api-Key"].Reveal() != "k1" {
		t.Errorf("secrets = %d entries", len(got))
	}
	if s := fmt.Sprint(got); strings.Contains(s, "u:p") || strings.Contains(s, "k1") {
		t.Errorf("printing the map leaks values: %s", s)
	}
}

func TestTargets(t *testing.T) {
	d := testDB(t)
	a := create(t, d, sample("a"))
	b := create(t, d, sample("b"))
	got, err := Targets(context.Background(), d.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[a] != "https://example.com/a" || got[b] != "https://example.com/b" {
		t.Errorf("Targets = %v", got)
	}
}
