package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestProfileRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	mail, _ := CreateChannel(ctx, d, k, smtpInput("Mail"), now)
	chat, _ := CreateChannel(ctx, d, k, smtpInput("Chat"), now)

	in := ProfileInput{Name: " Critical ", QuietEnabled: true, QuietStart: "23:00", QuietEnd: "07:00", CriticalBypass: true,
		ReminderAfter: time.Hour, Routes: map[string][]string{"info": {chat}, "critical": {mail, chat}}}
	id, err := CreateProfile(ctx, d, in, now)
	if err != nil {
		t.Fatal(err)
	}
	m := sample("api")
	m.NotificationProfileID = id
	if _, err := CreateMonitor(ctx, d, m, now); err != nil {
		t.Fatal(err)
	}
	p, err := GetProfile(ctx, d.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Critical" || !p.QuietEnabled || p.QuietStart != "23:00" || p.QuietEnd != "07:00" || !p.CriticalBypass ||
		p.ReminderAfter != time.Hour || p.Monitors != 1 {
		t.Errorf("profile = %+v", p)
	}
	// Channels in name order.
	if !slices.Equal(p.Routes["critical"], []string{chat, mail}) || !slices.Equal(p.Routes["info"], []string{chat}) || len(p.Routes["warning"]) != 0 {
		t.Errorf("routes = %v", p.Routes)
	}

	// An update replaces the routes; quiet hours off clears the times.
	in = ProfileInput{Name: "Quiet", Routes: map[string][]string{"warning": {mail}}}
	if err := UpdateProfile(ctx, d, id, in, now); err != nil {
		t.Fatal(err)
	}
	p, _ = GetProfile(ctx, d.Reader, id)
	if p.Name != "Quiet" || p.QuietEnabled || p.QuietStart != "" || p.CriticalBypass || p.ReminderAfter != 0 ||
		len(p.Routes) != 1 || !slices.Equal(p.Routes["warning"], []string{mail}) {
		t.Errorf("after update = %+v", p)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM notification_profiles WHERE quiet_start IS NULL AND reminder_after_seconds IS NULL`); n != 1 {
		t.Errorf("stored NULLs: %d", n)
	}

	// A deleted channel leaves the route.
	if err := DeleteChannel(ctx, d, mail); err != nil {
		t.Fatal(err)
	}
	if p, _ = GetProfile(ctx, d.Reader, id); len(p.Routes["warning"]) != 0 {
		t.Errorf("routes after channel delete = %v", p.Routes)
	}

	// Deleting the profile keeps the monitor, without a profile.
	if err := DeleteProfile(ctx, d, id); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM monitors WHERE notification_profile_id IS NULL`); n != 1 {
		t.Errorf("%d monitors without a profile", n)
	}
	if err := DeleteProfile(ctx, d, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v", err)
	}
	if err := UpdateProfile(ctx, d, id, ProfileInput{Name: "x"}, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of a deleted profile = %v", err)
	}
	if _, err := GetProfile(ctx, d.Reader, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("get = %v", err)
	}
}

func TestProfileValidation(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	if _, err := CreateProfile(ctx, d, ProfileInput{Name: "Ops"}, now); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		in    ProfileInput
		field string
	}{
		{ProfileInput{Name: " "}, "name"},
		{ProfileInput{Name: string(make([]rune, MaxNameLen+1))}, "name"},
		{ProfileInput{Name: "Ops"}, "name"},
		{ProfileInput{Name: "x", QuietEnabled: true, QuietStart: "25:00", QuietEnd: "07:00"}, "quiet_start"},
		{ProfileInput{Name: "x", QuietEnabled: true, QuietStart: "23:00"}, "quiet_end"},
		{ProfileInput{Name: "x", QuietEnabled: true, QuietStart: "07:00", QuietEnd: "07:00"}, "quiet_end"},
		{ProfileInput{Name: "x", ReminderAfter: 30 * time.Second}, "reminder"},
		{ProfileInput{Name: "x", ReminderAfter: 8 * 24 * time.Hour}, "reminder"},
		{ProfileInput{Name: "x", Routes: map[string][]string{"critical": {"nope"}}}, "routes"},
		{ProfileInput{Name: "x", Routes: map[string][]string{"urgent": nil}}, "routes"},
	}
	for _, c := range cases {
		_, err := CreateProfile(ctx, d, c.in, now)
		var fe FieldErrors
		if !errors.As(err, &fe) || fe[c.field] == "" {
			t.Errorf("%+v: %v, want an error for %s", c.in, err, c.field)
		}
	}
	if n := count(t, d, `SELECT COUNT(*) FROM notification_profiles`); n != 1 {
		t.Errorf("%d profiles after rejected input", n)
	}
	// Quiet hours off: the times are not checked.
	if _, err := CreateProfile(ctx, d, ProfileInput{Name: "Off", QuietStart: "bad"}, now); err != nil {
		t.Errorf("quiet hours off: %v", err)
	}
}
