package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/notify"
	"github.com/drilonrecica/sinjal/internal/vault"
)

func smtpInput(name string) ChannelInput {
	return ChannelInput{Name: name, Enabled: true, Config: notify.SMTP{Host: "smtp.example.com", Port: 587,
		Security: notify.SecuritySTARTTLS, Username: "u", Password: "TOPSECRET-PW", From: "a@b.co", To: []string{"ops@example.com"}}}
}

func TestChannelRoundTripAndEncryption(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)

	id, err := CreateChannel(ctx, d, k, smtpInput("  Ops mail "), now)
	if err != nil {
		t.Fatal(err)
	}
	c, cfg, err := GetChannel(ctx, d.Reader, k, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Ops mail" || c.Type != "smtp" || !c.Enabled || c.HealthState != "unknown" || c.LastSuccessAt != nil || !c.CreatedAt.Equal(now) {
		t.Errorf("channel = %+v", c)
	}
	if s := cfg.(notify.SMTP); s.Password != "TOPSECRET-PW" || s.Host != "smtp.example.com" || s.To[0] != "ops@example.com" {
		t.Errorf("config = %#v", s)
	}

	var enc []byte
	if err := d.Reader.QueryRow(`SELECT config_enc FROM notification_channels WHERE id = ?`, id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"TOPSECRET-PW", "smtp.example.com", "ops@example.com"} {
		if bytes.Contains(enc, []byte(leak)) {
			t.Errorf("config_enc contains %q in the clear", leak)
		}
	}
}

func TestChannelConfigIsBoundToItsChannel(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	a, _ := CreateChannel(ctx, d, k, smtpInput("a"), now)
	b, _ := CreateChannel(ctx, d, k, smtpInput("b"), now)

	// Copy a's envelope onto b: the AAD (the channel id) must refuse it.
	if _, err := d.Writer.Exec(`UPDATE notification_channels SET config_enc = (SELECT config_enc FROM notification_channels WHERE id = ?) WHERE id = ?`, a, b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := GetChannel(ctx, d.Reader, k, b); !errors.Is(err, vault.ErrDecrypt) {
		t.Errorf("a copied envelope opened: err = %v", err)
	}
	if _, _, err := GetChannel(ctx, d.Reader, k, a); err != nil {
		t.Errorf("the original must still open: %v", err)
	}
}

func TestListChannelsNeedsNoKeyAndSortsByName(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	for _, n := range []string{"zeta", "Alpha", "beta"} {
		if _, err := CreateChannel(ctx, d, k, smtpInput(n), now); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ListChannels(ctx, d.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != "Alpha" || got[1].Name != "beta" || got[2].Name != "zeta" {
		t.Errorf("list = %+v", got)
	}
}

func TestCreateChannelValidates(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)

	bad := ChannelInput{Name: " ", Config: notify.SMTP{}}
	_, err := CreateChannel(ctx, d, k, bad, now)
	var fe FieldErrors
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want FieldErrors", err)
	}
	for _, f := range []string{"name", "host", "port", "from", "to"} {
		if fe[f] == "" {
			t.Errorf("no message for %s: %v", f, fe)
		}
	}
	long := smtpInput(string(bytes.Repeat([]byte("x"), MaxNameLen+1)))
	if _, err := CreateChannel(ctx, d, k, long, now); !errors.As(err, &fe) || fe["name"] == "" {
		t.Errorf("a long name: %v", err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM notification_channels`); n != 0 {
		t.Errorf("%d channels stored by rejected creates", n)
	}
}

func TestUpdateChannelKeepsBlankSecretAndReplacesGivenOne(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	id, _ := CreateChannel(ctx, d, k, smtpInput("mail"), now)
	later := now.Add(time.Hour)

	in := smtpInput("renamed")
	in.Enabled = false
	in.Config = func() notify.Config {
		c := in.Config.(notify.SMTP)
		c.Password = ""
		c.Host = "mail2.example.com"
		return c
	}()
	if err := UpdateChannel(ctx, d, k, id, in, later); err != nil {
		t.Fatal(err)
	}
	c, cfg, err := GetChannel(ctx, d.Reader, k, id)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.(notify.SMTP)
	if c.Name != "renamed" || c.Enabled || s.Host != "mail2.example.com" || s.Password != "TOPSECRET-PW" {
		t.Errorf("after update: %+v %#v", c, s)
	}
	if !c.CreatedAt.Equal(now) || !c.UpdatedAt.Equal(later) {
		t.Errorf("created %v updated %v", c.CreatedAt, c.UpdatedAt)
	}

	in.Config = func() notify.Config { c := in.Config.(notify.SMTP); c.Password = "NEW-PW"; return c }()
	if err := UpdateChannel(ctx, d, k, id, in, later); err != nil {
		t.Fatal(err)
	}
	if _, cfg, _ = GetChannel(ctx, d.Reader, k, id); cfg.(notify.SMTP).Password != "NEW-PW" {
		t.Error("a given password was not stored")
	}
}

func TestUpdateChannelFailuresChangeNothing(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	id, _ := CreateChannel(ctx, d, k, smtpInput("mail"), now)

	bad := smtpInput("mail")
	bad.Config = func() notify.Config { c := bad.Config.(notify.SMTP); c.Port = 0; c.Password = ""; return c }()
	var fe FieldErrors
	if err := UpdateChannel(ctx, d, k, id, bad, now); !errors.As(err, &fe) || fe["port"] == "" {
		t.Fatalf("err = %v", err)
	}
	if _, cfg, _ := GetChannel(ctx, d.Reader, k, id); cfg.(notify.SMTP).Port != 587 {
		t.Error("a rejected update changed the stored config")
	}

	if err := UpdateChannel(ctx, d, k, "nope", smtpInput("x"), now); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	// The type of a channel never changes.
	tg := ChannelInput{Name: "t", Enabled: true, Config: notify.Telegram{BotToken: "1:a", ChatID: "1"}}
	if err := UpdateChannel(ctx, d, k, id, tg, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("type change: %v", err)
	}
}

func TestUpdateRequiresSecretWhenNoneStored(t *testing.T) {
	// A channel created without credentials gains a user name: the
	// password is then required, not inherited from nothing.
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	in := smtpInput("open relay")
	in.Config = func() notify.Config { c := in.Config.(notify.SMTP); c.Username, c.Password = "", ""; return c }()
	id, err := CreateChannel(ctx, d, k, in, now)
	if err != nil {
		t.Fatal(err)
	}
	in.Config = func() notify.Config { c := in.Config.(notify.SMTP); c.Username = "u"; return c }()
	var fe FieldErrors
	if err := UpdateChannel(ctx, d, k, id, in, now); !errors.As(err, &fe) || fe["password"] == "" {
		t.Errorf("err = %v", err)
	}
}

func TestDeleteChannelCascades(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	id, _ := CreateChannel(ctx, d, k, smtpInput("mail"), now)
	d.Writer.Exec(`INSERT INTO notification_profiles (id, name, created_at, updated_at) VALUES ('p', 'p', ?, ?)`, "t", "t")
	d.Writer.Exec(`INSERT INTO notification_routes (profile_id, severity, channel_id) VALUES ('p', 'critical', ?)`, id)

	if err := DeleteChannel(ctx, d, id); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM notification_routes`); n != 0 {
		t.Errorf("%d routes left", n)
	}
	if err := DeleteChannel(ctx, d, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, _, err := GetChannel(ctx, d.Reader, k, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if _, err := GetChannelInfo(ctx, d.Reader, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("info after delete: %v", err)
	}
}
