package store

import (
	"strings"
	"testing"
)

// A rollup step reads, probes and deletes along idx_check_results_monitor_time
// or the check_aggregates primary key, never by scanning a table. The raw
// range read is the history query (TestHistoryQueryPlan).
func TestRollupQueryPlans(t *testing.T) {
	d := testDB(t)
	cases := []struct {
		query, index string
	}{
		{`SELECT min(checked_at) FROM check_results WHERE monitor_id = ?`, "idx_check_results_monitor_time"},
		{`DELETE FROM check_results WHERE monitor_id = ? AND checked_at >= ? AND checked_at < ?`, "idx_check_results_monitor_time"},
		{`SELECT min(bucket_start) FROM check_aggregates WHERE monitor_id = ? AND resolution_seconds = ?`, "sqlite_autoindex_check_aggregates_1"},
		{`SELECT bucket_start FROM check_aggregates WHERE monitor_id = ? AND resolution_seconds = ?
			AND bucket_start >= ? AND bucket_start < ? ORDER BY bucket_start`, "sqlite_autoindex_check_aggregates_1"},
		{`DELETE FROM check_aggregates WHERE monitor_id = ? AND resolution_seconds = ?
			AND bucket_start >= ? AND bucket_start < ?`, "sqlite_autoindex_check_aggregates_1"},
	}
	for _, c := range cases {
		args := make([]any, strings.Count(c.query, "?"))
		for i := range args {
			args[i] = "x"
		}
		plan := queryPlan(t, d.Writer, c.query, args...)
		if !strings.Contains(plan, c.index) || strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("%s\nplan: %s", c.query, plan)
		}
	}
}
