// Package retention rolls check history into coarser tiers and deletes what
// has been rolled (docs/09_DATABASE.md "Retention jobs"): raw results older
// than 7 days into 5-minute buckets, those older than 30 days into hourly
// buckets, those older than 365 days into daily buckets, which are kept.
// The SQL of a step is store.RollupOldest; the daily runner calls Rollup.
package retention

import (
	"context"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/history"
	"github.com/drilonrecica/sinjal/internal/store"
)

// Tier rolls the rows of resolution Src older than Keep into resolution Dst.
type Tier struct {
	Name     string
	Src, Dst time.Duration // Src is store.RawTier for raw results
	Keep     time.Duration
}

// Tiers, in the order a run applies them.
var Tiers = []Tier{
	{Name: "raw", Src: store.RawTier, Dst: history.Res5m, Keep: 7 * 24 * time.Hour},
	{Name: "5m", Src: history.Res5m, Dst: history.Res1h, Keep: 30 * 24 * time.Hour},
	{Name: "1h", Src: history.Res1h, Dst: history.Res1d, Keep: 365 * 24 * time.Hour},
}

// Slice is the most source time one transaction rolls: a day of raw
// results is 2,880 rows at a 30 s interval, short enough that the result
// processor waiting for the writer does not notice.
const Slice = 24 * time.Hour

// Cutoff is the end of what tier t rolls as of now: Keep ago, floored to
// the target resolution so only whole buckets are rolled.
func (t Tier) Cutoff(now time.Time) time.Time {
	return history.Floor(now.Add(-t.Keep), t.Dst)
}

// Stats counts what a run did.
type Stats struct {
	Steps   int   // transactions committed
	Buckets int   // buckets written
	Deleted int64 // source rows deleted
}

// Rollup applies every tier to every monitor as of now. Each step is its
// own transaction that writes buckets and deletes their sources together,
// so stopping at any point (an error, ctx cancelled at shutdown) leaves
// consistent data, and the next run carries on from the oldest source row
// left: there is no cursor to keep. It stops at the first error.
func Rollup(ctx context.Context, d *db.DB, now time.Time) (Stats, error) {
	var st Stats
	ids, err := store.MonitorIDs(ctx, d.Reader)
	if err != nil {
		return st, err
	}
	for _, t := range Tiers {
		cutoff := t.Cutoff(now)
		for _, id := range ids {
			for {
				if err := ctx.Err(); err != nil {
					return st, err
				}
				step, ok, err := store.RollupOldest(ctx, d, id, t.Src, t.Dst, cutoff, Slice)
				if err != nil {
					return st, err
				}
				if !ok {
					break
				}
				st.Steps++
				st.Buckets += step.Buckets
				st.Deleted += step.Deleted
			}
		}
	}
	return st, nil
}
