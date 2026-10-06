package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/maintenance"
)

func TestMaintenanceCRUD(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	in := sample("api")
	in.Tags = []string{"Prod"}
	m := create(t, d, in)

	w := maintenance.Window{Name: "  Patch night ", Start: now.Add(90 * time.Minute).Add(300 * time.Millisecond), Duration: 2 * time.Hour,
		Recurrence: maintenance.Weekly, Weekdays: 0x81, Suppress: true, ExcludeUptime: false,
		Scope: maintenance.Scope{Monitors: []string{m, m}, Tags: []string{"prod"}}}
	id, err := CreateMaintenance(ctx, d, w, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := GetMaintenance(ctx, d.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	want := maintenance.Window{ID: id, Name: "Patch night", Start: now.Add(90 * time.Minute), Duration: 2 * time.Hour,
		Recurrence: maintenance.Weekly, Weekdays: 0x01, Suppress: true,
		Scope: maintenance.Scope{Monitors: []string{m}, Tags: []string{"Prod"}}, CreatedAt: now, UpdatedAt: now}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stored\n%+v\nwant\n%+v", got, want)
	}

	got.Recurrence, got.Scope = maintenance.Daily, maintenance.Scope{}
	if err := UpdateMaintenance(ctx, d, got, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM maintenance_windows WHERE id = ? AND scope_json IS NULL
		AND weekday_mask IS NULL AND recurrence = 'daily' AND updated_at = ?`, id, formatTime(now.Add(time.Hour))); n != 1 {
		t.Fatal("the update was not stored in canonical form")
	}
	list, err := ListMaintenance(ctx, d.Reader)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("ListMaintenance = %+v, %v", list, err)
	}

	got.ID = "missing"
	if err := UpdateMaintenance(ctx, d, got, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a missing window = %v", err)
	}
	if err := DeleteMaintenance(ctx, d, id); err != nil {
		t.Fatal(err)
	}
	if err := DeleteMaintenance(ctx, d, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	if _, err := GetMaintenance(ctx, d.Reader, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
}

func TestMaintenanceValidation(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	ok := maintenance.Window{Name: "x", Start: now, Duration: time.Hour, Recurrence: maintenance.None}
	for name, c := range map[string]struct {
		edit  func(*maintenance.Window)
		field string
	}{
		"no name":          {func(w *maintenance.Window) { w.Name = " " }, "name"},
		"long name":        {func(w *maintenance.Window) { w.Name = string(make([]byte, 101)) }, "name"},
		"no start":         {func(w *maintenance.Window) { w.Start = time.Time{} }, "starts_at"},
		"zero duration":    {func(w *maintenance.Window) { w.Duration = 0 }, "duration"},
		"too long":         {func(w *maintenance.Window) { w.Duration = 32 * 24 * time.Hour }, "duration"},
		"weekly no days":   {func(w *maintenance.Window) { w.Recurrence, w.Weekdays = maintenance.Weekly, 0x80 }, "weekdays"},
		"bad recurrence":   {func(w *maintenance.Window) { w.Recurrence = "monthly" }, "recurrence"},
		"unknown monitor":  {func(w *maintenance.Window) { w.Scope.Monitors = []string{"nope"} }, "scope"},
		"unknown tag name": {func(w *maintenance.Window) { w.Scope.Tags = []string{"nope"} }, "scope"},
	} {
		w := ok
		c.edit(&w)
		_, err := CreateMaintenance(ctx, d, w, now)
		var fe FieldErrors
		if !errors.As(err, &fe) || fe[c.field] == "" || len(fe) != 1 {
			t.Errorf("%s: err = %v, want one error on %s", name, err, c.field)
		}
	}
	if n := count(t, d, `SELECT COUNT(*) FROM maintenance_windows`); n != 0 {
		t.Fatalf("%d windows stored from invalid input", n)
	}
}

func TestMonitorWindows(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	tagged := sample("tagged")
	tagged.Tags = []string{"edge"}
	a, b, c := create(t, d, sample("a")), create(t, d, tagged), create(t, d, sample("c"))
	add := func(name string, s maintenance.Scope) {
		if _, err := CreateMaintenance(ctx, d, maintenance.Window{Name: name, Start: now, Duration: time.Hour,
			Recurrence: maintenance.None, Scope: s}, now); err != nil {
			t.Fatal(err)
		}
	}
	add("all", maintenance.Scope{})
	add("a only", maintenance.Scope{Monitors: []string{a}})
	add("edge", maintenance.Scope{Tags: []string{"EDGE"}})
	for id, want := range map[string][]string{a: {"a only", "all"}, b: {"all", "edge"}, c: {"all"}} {
		ws, err := MonitorWindows(ctx, d.Reader, id)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, w := range ws {
			names = append(names, w.Name)
		}
		if !reflect.DeepEqual(names, want) {
			t.Errorf("windows of %s = %v, want %v", id, names, want)
		}
	}
}
