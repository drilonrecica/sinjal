package incident

import (
	"math"
	"time"
)

// DaysLeft is how many whole days a certificate still has at the time:
// 0 on its last day, negative once it has expired.
func DaysLeft(notAfter, at time.Time) int {
	return int(math.Floor(notAfter.Sub(at).Hours() / 24))
}

// CrossedThresholds returns the warning thresholds (in days) a certificate
// has reached at the time: every threshold of at least its days left. A
// certificate first seen close to expiry reaches several at once.
func CrossedThresholds(thresholds []int, notAfter, at time.Time) []int {
	left := DaysLeft(notAfter, at)
	var out []int
	for _, d := range thresholds {
		if d >= left {
			out = append(out, d)
		}
	}
	return out
}
