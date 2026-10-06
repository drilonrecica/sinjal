package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// BenchmarkInsertCheckResult is docs/18 scenario 4: raw check_results
// inserts the way the result processor makes them, 128 to a transaction.
// One operation is one row; the commit is shared by its batch.
func BenchmarkInsertCheckResult(b *testing.B) {
	d := testDB(b)
	ctx := context.Background()
	id := create(b, d, sample("a"))
	const batch = 128
	var tx *sql.Tx
	commit := func() {
		if tx != nil {
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			tx = nil
		}
	}
	at := time.Now()
	n := 0
	b.ReportAllocs()
	for b.Loop() {
		if tx == nil {
			var err error
			if tx, err = d.Writer.BeginTx(ctx, nil); err != nil {
				b.Fatal(err)
			}
		}
		r := CheckResult{MonitorID: id, CheckedAt: at.Add(time.Duration(n) * time.Second), Duration: 42 * time.Millisecond, Success: true, ProtocolStatus: "200"}
		if n%10 == 0 {
			r.Success, r.ProtocolStatus, r.ErrorKind, r.ErrorMessage, r.Snippet = false, "503", "http_status", "status 503, expected 200-399", "<h1>maintenance</h1>"
		}
		if err := InsertCheckResult(ctx, tx, r); err != nil {
			b.Fatal(err)
		}
		n++
		if n%batch == 0 {
			commit()
		}
	}
	commit()
}
