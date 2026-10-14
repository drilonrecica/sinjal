package store

import (
	"context"
	"database/sql"
	"slices"
	"time"
)

// RecentCheck is one stored check as a beat bar shows it.
type RecentCheck struct {
	CheckedAt time.Time
	Duration  time.Duration
	Success   bool
	Kind      string // failure kind; "" on success
}

// RecentChecks returns the newest n checks of every monitor that has any,
// oldest first, to seed the engine's in-memory beat bars at start
// (internal/recent). One index probe per monitor, in one statement; about
// 300 ms for 1,000 monitors × 120, nearly all of it the driver handing
// rows over, which is why the engine seeds in the background.
func RecentChecks(ctx context.Context, q *sql.DB, n int) (map[string][]RecentCheck, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.monitor_id, r.checked_at, r.duration_ms, r.success, COALESCE(r.error_kind, '')
		FROM monitors m JOIN check_results r ON r.id IN (
			SELECT id FROM check_results WHERE monitor_id = m.id ORDER BY checked_at DESC, id DESC LIMIT ?)
`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]RecentCheck{}
	for rows.Next() {
		var id, at string
		var ms sql.NullFloat64
		var c RecentCheck
		if err := rows.Scan(&id, &at, &ms, &c.Success, &c.Kind); err != nil {
			return nil, err
		}
		c.CheckedAt, c.Duration = parseTime(at), msToDuration(ms.Float64)
		out[id] = append(out[id], c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Sorting each monitor's few checks here is cheaper than an ORDER BY
	// over all of them (about 90 of 380 ms at 1,000 monitors).
	for _, cs := range out {
		slices.SortStableFunc(cs, func(a, b RecentCheck) int { return a.CheckedAt.Compare(b.CheckedAt) })
	}
	return out, nil
}
