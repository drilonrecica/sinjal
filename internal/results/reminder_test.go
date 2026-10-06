package results

import (
	"testing"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/store"
)

// withReminder creates a monitor whose profile reminds after the given
// seconds (no reminder when 0).
func withReminder(t *testing.T, d *db.DB, name string, after int) string {
	t.Helper()
	profile := "p-" + name
	var seconds any
	if after > 0 {
		seconds = after
	}
	if _, err := d.Writer.Exec(`INSERT INTO notification_profiles (id, name, reminder_after_seconds, created_at, updated_at)
		VALUES (?, ?, ?, 'now', 'now')`, profile, profile, seconds); err != nil {
		t.Fatal(err)
	}
	return newMonitor(t, d, name, func(m *store.MonitorInput) { m.NotificationProfileID = profile })
}

func reminderSentAt(t *testing.T, d *db.DB, monitorID string) []string {
	t.Helper()
	rows, err := d.Reader.Query(`SELECT COALESCE(reminder_sent_at, '-') FROM incidents WHERE monitor_id = ? ORDER BY started_at`, monitorID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// The reminder is decided on the first check at or after the incident's
// start plus the duration, once, and not again for the same incident, also
// after a restart; the next incident gets its own.
func TestReminderOncePerIncident(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := withReminder(t, d, "web", 60)

	h.feed(fail(id, 1), fail(id, 2), fail(id, 30), fail(id, 60))
	same(t, "before the duration", h.intentsOf(id), []string{"down 2 -"})
	h.feed(fail(id, 61))
	same(t, "at the duration", h.intentsOf(id), []string{"down 2 -", "reminder 61 -"})
	same(t, "marked", reminderSentAt(t, d, id), []string{store.FormatTime(at(61))})
	h.feed(fail(id, 90), fail(id, 600))
	h.stop()

	h = newHarness(t, d).run()
	h.feed(fail(id, 900), ok(id, 901))
	same(t, "after a restart", h.intentsOf(id), []string{"recovery 901 -"})

	h.feed(fail(id, 1000), fail(id, 1001), fail(id, 1061))
	same(t, "the next incident", h.intentsOf(id), []string{"recovery 901 -", "down 1001 -", "reminder 1061 -"})
}

// No profile, a profile without a duration, or an outage shorter than the
// duration: no reminder, nothing marked.
func TestNoReminder(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	plain := newMonitor(t, d, "plain", nil)
	off := withReminder(t, d, "off", 0)
	short := withReminder(t, d, "short", 300)

	for _, id := range []string{plain, off, short} {
		h.feed(fail(id, 1), fail(id, 2), fail(id, 400))
	}
	same(t, "no profile", h.intentsOf(plain), []string{"down 2 -"})
	same(t, "no duration", h.intentsOf(off), []string{"down 2 -"})
	same(t, "due", h.intentsOf(short), []string{"down 2 -", "reminder 400 -"})

	recovered := withReminder(t, d, "recovered", 300)
	h.feed(fail(recovered, 1), fail(recovered, 2), ok(recovered, 299), fail(recovered, 400))
	same(t, "recovered first", h.intentsOf(recovered), []string{"down 2 -", "recovery 299 -"})
	same(t, "nothing marked", reminderSentAt(t, d, recovered), []string{"-"})
}

// A reminder that falls due while the monitor flaps is suppressed, recorded
// on the timeline and not sent later.
func TestReminderSuppressedWhileFlapping(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := withReminder(t, d, "web", 60)
	h.feed(fail(id, 1), fail(id, 2))
	if _, err := d.Writer.Exec(`UPDATE monitors SET flapping_since = ? WHERE id = ?`, store.FormatTime(at(2)), id); err != nil {
		t.Fatal(err)
	}
	h.stop() // the overlay is read when a batch loads the monitor
	h = newHarness(t, d).run()
	h.feed(fail(id, 62), fail(id, 120))
	same(t, "intents", h.intentsOf(id), []string{"reminder 62 flapping"})
	same(t, "events", events(t, d, id)[2:], []string{"notification_suppressed 62 reminder: flapping"})
}

// Inside a maintenance window the reminder is suppressed; the window's end
// does not bring it back.
func TestReminderSuppressedByMaintenance(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := withReminder(t, d, "web", 60)
	h.feed(fail(id, 1), fail(id, 2))
	window(t, d, 50, 150, true)
	h.feed(fail(id, 70), fail(id, 160))
	same(t, "intents", h.intentsOf(id), []string{"down 2 -", "reminder 70 maintenance"})
}

// A shorter duration set while the incident runs applies to it.
func TestReminderDurationChanged(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := withReminder(t, d, "web", 3600)
	h.feed(fail(id, 1), fail(id, 2), fail(id, 120))
	if _, err := d.Writer.Exec(`UPDATE notification_profiles SET reminder_after_seconds = 60`); err != nil {
		t.Fatal(err)
	}
	h.feed(fail(id, 150))
	same(t, "intents", h.intentsOf(id), []string{"down 2 -", "reminder 150 -"})
}
