package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

func viewUser(t *testing.T, d *db.DB, id string) {
	t.Helper()
	if _, err := d.Writer.Exec(`INSERT INTO users (id, login, role, created_at, updated_at) VALUES (?, ?, 'viewer', 'x', 'x')`, id, id); err != nil {
		t.Fatal(err)
	}
}

func TestSavedViewsCRUDPerUser(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	viewUser(t, d, "u1")
	viewUser(t, d, "u2")

	f := ViewFilters{Text: " api ", States: []string{"Down", "up", "down"}, Tags: []string{"Hetzner", "hetzner"}, Failing: true}
	v, err := CreateSavedView(ctx, d, "u1", " Infra ", f, SortSlowest, now)
	if err != nil {
		t.Fatal(err)
	}
	want := ViewFilters{Text: "api", States: []string{"down", "up"}, Tags: []string{"Hetzner"}, Failing: true}
	if v.Name != "Infra" || !reflect.DeepEqual(v.Filters, want) {
		t.Fatalf("created = %+v", v)
	}

	got, err := ListSavedViews(ctx, d.Reader, "u1")
	if err != nil || len(got) != 1 || got[0].Name != "Infra" || got[0].Sort != SortSlowest || !reflect.DeepEqual(got[0].Filters, want) {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if other, _ := ListSavedViews(ctx, d.Reader, "u2"); len(other) != 0 {
		t.Errorf("another user sees %d views", len(other))
	}

	// Names are unique per user, ignoring case; another user may reuse one.
	var in *InputError
	if _, err := CreateSavedView(ctx, d, "u1", "infra", ViewFilters{}, "", now); !errors.As(err, &in) || in.Field != "name" {
		t.Errorf("duplicate name = %v", err)
	}
	if _, err := CreateSavedView(ctx, d, "u2", "infra", ViewFilters{}, "", now); err != nil {
		t.Errorf("same name for another user: %v", err)
	}

	// Another user's view cannot be changed or removed.
	if err := UpdateSavedView(ctx, d, "u2", v.ID, "x", ViewFilters{}, "", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign update = %v", err)
	}
	if err := DeleteSavedView(ctx, d, "u2", v.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign delete = %v", err)
	}

	later := now.Add(time.Hour)
	if err := UpdateSavedView(ctx, d, "u1", v.ID, "Infra 2", ViewFilters{Types: []string{"tcp"}}, "", later); err != nil {
		t.Fatal(err)
	}
	got, _ = ListSavedViews(ctx, d.Reader, "u1")
	if got[0].Name != "Infra 2" || got[0].Sort != "" || !reflect.DeepEqual(got[0].Filters.Types, []string{"tcp"}) || !got[0].UpdatedAt.Equal(later) {
		t.Errorf("updated = %+v", got[0])
	}
	if err := DeleteSavedView(ctx, d, "u1", v.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := ListSavedViews(ctx, d.Reader, "u1"); len(got) != 0 {
		t.Errorf("after delete: %+v", got)
	}
}

func TestSavedViewValidation(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	viewUser(t, d, "u1")
	for name, c := range map[string]struct {
		name string
		f    ViewFilters
		sort string
	}{
		"empty name":    {"  ", ViewFilters{}, ""},
		"long name":     {strings.Repeat("x", MaxViewName+1), ViewFilters{}, ""},
		"control name":  {"a\nb", ViewFilters{}, ""},
		"unknown state": {"v", ViewFilters{States: []string{"sideways"}}, ""},
		"unknown type":  {"v", ViewFilters{Types: []string{"ftp"}}, ""},
		"bad tag":       {"v", ViewFilters{Tags: []string{"a,b"}}, ""},
		"long text":     {"v", ViewFilters{Text: strings.Repeat("x", MaxViewText+1)}, ""},
		"unknown sort":  {"v", ViewFilters{}, "random"},
	} {
		var in *InputError
		if _, err := CreateSavedView(ctx, d, "u1", c.name, c.f, c.sort, now); !errors.As(err, &in) {
			t.Errorf("%s: err = %v, want an InputError", name, err)
		}
	}
	if n := count(t, d, `SELECT COUNT(*) FROM saved_views`); n != 0 {
		t.Errorf("%d views stored after refusals", n)
	}
}

func TestSavedViewLimitAndUserDelete(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	viewUser(t, d, "u1")
	for i := range MaxUserViews {
		if _, err := CreateSavedView(ctx, d, "u1", "v"+string(rune('a'+i)), ViewFilters{}, "", now); err != nil {
			t.Fatal(err)
		}
	}
	var in *InputError
	if _, err := CreateSavedView(ctx, d, "u1", "one more", ViewFilters{}, "", now); !errors.As(err, &in) {
		t.Errorf("over the limit = %v", err)
	}
	// Views go with their user.
	if _, err := d.Writer.Exec(`DELETE FROM users WHERE id = 'u1'`); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM saved_views`); n != 0 {
		t.Errorf("%d views left after the user was deleted", n)
	}
}
