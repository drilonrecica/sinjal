package audit

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func testDB(t *testing.T) *db.DB {
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

func TestWriteAndList(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	if _, err := d.Writer.Exec(`INSERT INTO users (id, login, role, created_at, updated_at) VALUES ('u1', 'admin', 'admin', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}

	if err := Record(ctx, d, Event{UserID: "u1", Type: ViewerCreated, ObjectType: "user", ObjectID: "v1", Metadata: map[string]string{"client_ip": "1.2.3.4"}}, now); err != nil {
		t.Fatal(err)
	}
	// An event with nobody signed in: every optional column is NULL.
	if err := Record(ctx, d, Event{Type: LoginFailed}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	got, next, err := List(ctx, d.Reader, 0, 10)
	if err != nil || next != 0 || len(got) != 2 {
		t.Fatalf("List = %d entries, next %d, %v", len(got), next, err)
	}
	if got[0].Type != LoginFailed || got[0].Actor != "" || got[0].Metadata != nil {
		t.Errorf("newest = %+v", got[0])
	}
	e := got[1]
	if e.Type != ViewerCreated || e.Actor != "admin" || e.ObjectType != "user" || e.ObjectID != "v1" || e.Metadata["client_ip"] != "1.2.3.4" || !e.At.Equal(now) {
		t.Errorf("oldest = %+v", e)
	}

	// Deleting the actor keeps the event (ON DELETE SET NULL).
	if _, err := d.Writer.Exec(`DELETE FROM users WHERE id = 'u1'`); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := List(ctx, d.Reader, 0, 10); len(got) != 2 || got[1].Actor != "" {
		t.Errorf("after deleting the actor: %+v", got)
	}
}

func TestWriteJoinsTransaction(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	tx, err := d.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(ctx, tx, Event{Type: LoginFailed}, now); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if got, _, _ := List(ctx, d.Reader, 0, 10); len(got) != 0 {
		t.Errorf("a rolled-back change left %d events", len(got))
	}
}

func TestListPaging(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	for i := range 7 {
		if err := Record(ctx, d, Event{Type: LoginFailed, ObjectID: string(rune('a' + i))}, now); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	var cursor int64
	pages := 0
	for {
		got, next, err := List(ctx, d.Reader, cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, e := range got {
			seen = append(seen, e.ObjectID)
		}
		if next == 0 {
			break
		}
		// A new event arriving between pages must not shift older pages.
		if pages == 1 {
			Record(ctx, d, Event{Type: LoginFailed, ObjectID: "new"}, now)
		}
		cursor = next
	}
	if pages != 3 || strings.Join(seen, "") != "gfedcba" {
		t.Errorf("%d pages, saw %q; want 3 pages, gfedcba", pages, strings.Join(seen, ""))
	}
}

// A malformed metadata value still lists; it only loses its details.
func TestListToleratesBadMetadata(t *testing.T) {
	d := testDB(t)
	if _, err := d.Writer.Exec(`INSERT INTO audit_events (event_type, metadata_json, created_at) VALUES ('x', '{nope', '2026-10-06T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	got, _, err := List(context.Background(), d.Reader, 0, 10)
	if err != nil || len(got) != 1 || got[0].Metadata != nil {
		t.Errorf("List = %+v, %v", got, err)
	}
}
