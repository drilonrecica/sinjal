package scheduler

import (
	"hash/fnv"
	"time"
)

// maxJitterCap bounds the offset for long intervals.
const maxJitterCap = 30 * time.Second

// maxJitter is the largest offset for an interval: a tenth of it, at most
// 30 s (1 s for 10 s, 3 s for 30 s, 6 s for 60 s, 30 s from 5 min up).
func maxJitter(interval time.Duration) time.Duration {
	return min(interval/10, maxJitterCap)
}

// jitter is a monitor's offset within [0, maxJitter]. It depends only on
// the id and the interval, so it is the same after every restart and edit,
// and it is applied once, as a shift of the whole schedule: monitors that
// were scheduled at the same moment do not fire on the same instant, yet
// consecutive checks of one monitor stay exactly one interval apart.
func jitter(id string, interval time.Duration) time.Duration {
	limit := maxJitter(interval)
	if limit <= 0 {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(id))
	return time.Duration(h.Sum64() % uint64(limit+1))
}
