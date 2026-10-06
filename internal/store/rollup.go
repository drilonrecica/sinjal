package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/history"
)

// Rollups (docs/09_DATABASE.md "Retention jobs"). One step rolls one slice
// of one monitor's oldest source rows into the next tier and deletes them,
// in one write transaction: either the buckets are written and their
// sources gone, or nothing changed. A step is small (at most span of
// source time) because the writer connection is shared with the result
// processor, which waits while the step holds it. The policy (which tier,
// which cutoff) is internal/retention's.

// RawTier is the source resolution of raw check results.
const RawTier time.Duration = 0

// RollupStep is what one step did.
type RollupStep struct {
	From, To time.Time // the slice rolled, [From, To)
	Buckets  int       // buckets written
	Deleted  int64     // source rows deleted
}

// RollupOldest rolls a monitor's oldest source rows of resolution src
// (RawTier for check_results) into buckets of resolution dst: the slice
// starts at the bucket holding the oldest row and ends span later or at
// cutoff, whichever is first. cutoff and span must be multiples of dst, so
// only whole buckets are rolled. ok is false when no source row is older
// than cutoff. Buckets are upserted, so a step that runs again for a
// bucket replaces it.
func RollupOldest(ctx context.Context, d *db.DB, monitorID string, src, dst time.Duration, cutoff time.Time, span time.Duration) (step RollupStep, ok bool, err error) {
	err = db.Retry(ctx, func() error {
		step, ok = RollupStep{}, false
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		oldest, found, err := oldestSource(ctx, tx, monitorID, src)
		if err != nil || !found || !oldest.Before(cutoff) {
			return err
		}
		step.From = history.Floor(oldest, dst)
		step.To = step.From.Add(span)
		if step.To.After(cutoff) {
			step.To = cutoff
		}
		var buckets []history.Bucket
		if src == RawTier {
			buckets, err = rawBuckets(ctx, tx, monitorID, step.From, step.To, dst)
		} else {
			buckets, err = aggregateBuckets(ctx, tx, monitorID, src, step.From, step.To, dst)
		}
		if err != nil {
			return err
		}
		if err := upsertAggregates(ctx, tx, monitorID, dst, buckets); err != nil {
			return err
		}
		if step.Deleted, err = deleteSources(ctx, tx, monitorID, src, step.From, step.To); err != nil {
			return err
		}
		// The oldest row is inside the slice, so a step that deletes nothing
		// would be repeated forever by the caller's loop.
		if step.Deleted == 0 {
			return fmt.Errorf("rollup of %s at %s deleted no source rows", monitorID, formatTime(oldest))
		}
		step.Buckets, ok = len(buckets), true
		return tx.Commit()
	})
	return step, ok, err
}

// MonitorIDs lists every monitor, paused ones included.
func MonitorIDs(ctx context.Context, q querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM monitors ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// oldestSource is the time of a monitor's oldest source row: one probe of
// idx_check_results_monitor_time or the check_aggregates primary key.
func oldestSource(ctx context.Context, q querier, monitorID string, src time.Duration) (time.Time, bool, error) {
	var at sql.NullString
	var err error
	if src == RawTier {
		err = q.QueryRowContext(ctx, `SELECT min(checked_at) FROM check_results WHERE monitor_id = ?`, monitorID).Scan(&at)
	} else {
		err = q.QueryRowContext(ctx, `SELECT min(bucket_start) FROM check_aggregates
			WHERE monitor_id = ? AND resolution_seconds = ?`, monitorID, seconds(src)).Scan(&at)
	}
	if err != nil || !at.Valid {
		return time.Time{}, false, err
	}
	return parseTime(at.String), true, nil
}

// rawBuckets reads [from, to) of check_results in index order and builds a
// bucket of resolution res for each run of rows in one bucket.
func rawBuckets(ctx context.Context, tx *sql.Tx, monitorID string, from, to time.Time, res time.Duration) ([]history.Bucket, error) {
	rows, err := tx.QueryContext(ctx, `SELECT checked_at, success, duration_ms FROM check_results
		WHERE monitor_id = ? AND checked_at >= ? AND checked_at < ? ORDER BY checked_at`,
		monitorID, formatTime(from), formatTime(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []history.Bucket
	var start time.Time
	var samples []history.Sample
	flush := func() {
		if len(samples) > 0 {
			out = append(out, history.FromRaw(start, samples))
			samples = samples[:0]
		}
	}
	for rows.Next() {
		var at string
		var s history.Sample
		var ms sql.NullFloat64
		if err := rows.Scan(&at, &s.OK, &ms); err != nil {
			return nil, err
		}
		s.MS = ms.Float64
		if b := history.Floor(parseTime(at), res); !b.Equal(start) {
			flush()
			start = b
		}
		samples = append(samples, s)
	}
	flush()
	return out, rows.Err()
}

// aggregateBuckets reads [from, to) of the src tier in key order and
// merges the buckets falling into each bucket of resolution res.
func aggregateBuckets(ctx context.Context, tx *sql.Tx, monitorID string, src time.Duration, from, to time.Time, res time.Duration) ([]history.Bucket, error) {
	children, err := readAggregates(ctx, tx, `SELECT bucket_start, total_count, success_count, failure_count,
		min_ms, max_ms, avg_ms, p95_ms FROM check_aggregates
		WHERE monitor_id = ? AND resolution_seconds = ? AND bucket_start >= ? AND bucket_start < ?
		ORDER BY bucket_start`, monitorID, seconds(src), formatTime(from), formatTime(to))
	if err != nil {
		return nil, err
	}
	var out []history.Bucket
	for i := 0; i < len(children); {
		start := history.Floor(children[i].Start, res)
		j := i
		for j < len(children) && history.Floor(children[j].Start, res).Equal(start) {
			j++
		}
		out = append(out, history.Merge(start, children[i:j]))
		i = j
	}
	return out, nil
}

// readAggregates scans check_aggregates rows selected in the column order
// of aggregateBuckets.
func readAggregates(ctx context.Context, q querier, query string, args ...any) ([]history.Bucket, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []history.Bucket
	for rows.Next() {
		var b history.Bucket
		var start string
		var lo, hi, avg, p95 sql.NullFloat64
		if err := rows.Scan(&start, &b.Total, &b.Success, &b.Failure, &lo, &hi, &avg, &p95); err != nil {
			return nil, err
		}
		b.Start = parseTime(start)
		b.Min, b.Max, b.Avg, b.P95 = lo.Float64, hi.Float64, avg.Float64, p95.Float64
		out = append(out, b)
	}
	return out, rows.Err()
}

// upsertAggregates writes buckets of resolution res, replacing any with
// the same start. A bucket without a success stores NULL latency.
func upsertAggregates(ctx context.Context, tx *sql.Tx, monitorID string, res time.Duration, buckets []history.Bucket) error {
	if len(buckets) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO check_aggregates
		(monitor_id, resolution_seconds, bucket_start, total_count, success_count, failure_count,
		 min_ms, max_ms, avg_ms, p95_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (monitor_id, resolution_seconds, bucket_start) DO UPDATE SET
		 total_count = excluded.total_count, success_count = excluded.success_count,
		 failure_count = excluded.failure_count, min_ms = excluded.min_ms, max_ms = excluded.max_ms,
		 avg_ms = excluded.avg_ms, p95_ms = excluded.p95_ms`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, b := range buckets {
		var lo, hi, avg, p95 any
		if b.Success > 0 {
			lo, hi, avg, p95 = b.Min, b.Max, b.Avg, b.P95
		}
		if _, err := stmt.ExecContext(ctx, monitorID, seconds(res), formatTime(b.Start),
			b.Total, b.Success, b.Failure, lo, hi, avg, p95); err != nil {
			return err
		}
	}
	return nil
}

// deleteSources deletes the src tier's rows of [from, to).
func deleteSources(ctx context.Context, tx *sql.Tx, monitorID string, src time.Duration, from, to time.Time) (int64, error) {
	var res sql.Result
	var err error
	if src == RawTier {
		res, err = tx.ExecContext(ctx, `DELETE FROM check_results
			WHERE monitor_id = ? AND checked_at >= ? AND checked_at < ?`, monitorID, formatTime(from), formatTime(to))
	} else {
		res, err = tx.ExecContext(ctx, `DELETE FROM check_aggregates
			WHERE monitor_id = ? AND resolution_seconds = ? AND bucket_start >= ? AND bucket_start < ?`,
			monitorID, seconds(src), formatTime(from), formatTime(to))
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func seconds(d time.Duration) int64 { return int64(d / time.Second) }
