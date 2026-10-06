package results

import (
	"fmt"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/store"
)

const day = 24 * time.Hour

// withCert is result r having seen a certificate that expires at notAfter.
func withCert(r Result, notAfter time.Time) Result {
	r.TLSNotAfter = &notAfter
	return r
}

// dayN is n days after base, in seconds.
func dayN(n int) int { return n * 86400 }

func tlsRows(t *testing.T, d *db.DB, monitorID string) []string {
	t.Helper()
	rows, err := d.Reader.Query(`SELECT cert_not_after, threshold_days FROM tls_warnings WHERE monitor_id = ?
		ORDER BY cert_not_after, threshold_days DESC`, monitorID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cert string
		var days int
		if err := rows.Scan(&cert, &days); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s %d", cert[:10], days))
	}
	return out
}

// One warning per threshold per certificate: crossing 30 days, then 14,
// never again for the same threshold, also after a restart; a renewed
// certificate resets the thresholds.
func TestTLSWarningPerThreshold(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	// Checked on whole days, the certificate has 40 - n days and an hour.
	cert := base.Add(40*day + time.Hour)

	h.feed(withCert(ok(id, 1), cert), withCert(ok(id, dayN(9)), cert))
	same(t, "40 and 31 days left", h.intentsOf(id), nil)
	h.feed(withCert(ok(id, dayN(10)), cert), withCert(ok(id, dayN(11)), cert), withCert(fail(id, dayN(20)), cert))
	same(t, "30 days left", h.intentsOf(id), []string{"tls_warning 864000 -"})
	h.feed(withCert(ok(id, dayN(26)), cert))
	same(t, "14 days left", h.intentsOf(id), []string{"tls_warning 864000 -", "tls_warning 2246400 -"})
	h.stop()

	h = newHarness(t, d).run()
	h.feed(withCert(ok(id, dayN(27)), cert))
	same(t, "after a restart", h.intentsOf(id), nil)

	renewed := base.Add(120*day + time.Hour)
	h.feed(withCert(ok(id, dayN(28)), renewed))
	same(t, "renewed", h.intentsOf(id), nil)
	same(t, "rows before the renewed one crosses", tlsRows(t, d, id), []string{"2026-11-15 30", "2026-11-15 14"})
	h.feed(withCert(ok(id, dayN(90)), renewed))
	same(t, "renewed, 30 days left", h.intentsOf(id), []string{"tls_warning 7776000 -"})
	same(t, "rows", tlsRows(t, d, id), []string{"2027-02-03 30"})
}

// A certificate first seen with 5 days left reaches every threshold at
// once: one warning, all three recorded.
func TestTLSWarningsTogether(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	h.feed(withCert(ok(id, 1), base.Add(5*day)), withCert(ok(id, 2), base.Add(5*day)))
	same(t, "intents", h.intentsOf(id), []string{"tls_warning 1 -"})
	if n := len(tlsRows(t, d, id)); n != 3 {
		t.Fatalf("%d thresholds recorded, want 3", n)
	}
}

// Warnings turned off, or a monitor of another type: nothing, even if a
// result carries an expiry.
func TestTLSWarningsOff(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	off := newMonitor(t, d, "off", func(m *store.MonitorInput) { m.HTTP.TLSExpiryEnabled = false })
	tcp := newMonitor(t, d, "tcp", func(m *store.MonitorInput) {
		m.Type, m.HTTP, m.TCP = store.TypeTCP, store.HTTPConfig{}, store.TCPConfig{Host: "example.com", Port: 443}
	})
	for _, id := range []string{off, tcp} {
		h.feed(withCert(ok(id, 1), base.Add(day)))
		same(t, "intents", h.intentsOf(id), nil)
	}
}

// Maintenance suppresses the warning; the threshold stays recorded, so the
// window's end does not bring it back.
func TestTLSWarningSuppressedByMaintenance(t *testing.T) {
	d, _ := testDB(t)
	h := newHarness(t, d).run()
	id := newMonitor(t, d, "web", nil)
	window(t, d, 0, 100, true)
	cert := base.Add(20 * day)
	h.feed(withCert(ok(id, 1), cert), withCert(ok(id, 200), cert))
	same(t, "intents", h.intentsOf(id), []string{"tls_warning 1 maintenance"})
}
