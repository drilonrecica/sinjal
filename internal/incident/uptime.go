package incident

import (
	"fmt"
	"slices"
	"time"
)

// Span is the time from From up to, not including, To.
type Span struct{ From, To time.Time }

// UptimeInput is what the uptime of one monitor over a range is computed
// from (docs/10_INCIDENTS.md "Uptime"): intervals, not check results, so
// the same computation holds whatever raw history retention has removed.
type UptimeInput struct {
	Range   Span      // requested
	Created time.Time // the monitor's creation
	Now     time.Time
	Paused  []Span // pause intervals; an open one ends at Now
	Down    []Span // incidents; an active one ends at Now
	// Excluded are the maintenance occurrences excluded from adjusted
	// uptime.
	Excluded []Span
}

// Ratio is up time over observed time, both in whole seconds. Den 0 means
// there is no data: nothing of the range was observed.
type Ratio struct {
	Up, Den int64
}

// OK reports whether there is data.
func (r Ratio) OK() bool { return r.Den > 0 }

// Value is the ratio from 0 to 1; 0 without data.
func (r Ratio) Value() float64 {
	if r.Den == 0 {
		return 0
	}
	return float64(r.Up) / float64(r.Den)
}

// Percent is the ratio as a percentage with two decimals, truncated, so
// that any downtime at all never shows as 100.00%; "—" without data.
func (r Ratio) Percent() string {
	if r.Den == 0 {
		return "—"
	}
	h := r.Up * 10000 / r.Den // hundredths of a percent, truncated
	return fmt.Sprintf("%d.%02d%%", h/100, h%100)
}

// Uptime computes raw and adjusted uptime:
//
//	R = the range clipped to [Created, Now)
//	O = R minus paused time                      observed
//	D = downtime within O
//	M = excluded maintenance within O
//	raw      = (O − D) / O
//	adjusted = (O − M − (D − D∩M)) / (O − M)
//
// Times are taken in whole seconds, as they are stored: rounded down. Now
// is rounded up, because the current second has begun: a monitor created
// and checked within one second has been observed for that second.
func Uptime(in UptimeInput) (raw, adjusted Ratio) {
	now := CeilSecond(in.Now)
	r := Span{latest(in.Range.From, in.Created).Truncate(time.Second), CeilSecond(earliest(in.Range.To, now))}
	if !r.To.After(r.From) {
		return Ratio{}, Ratio{}
	}
	observed := subtract([]Span{r}, normalize(in.Paused, now))
	down := intersect(normalize(in.Down, now), observed)
	excluded := intersect(normalize(in.Excluded, now), observed)
	o, d, m := total(observed), total(down), total(excluded)
	dm := total(intersect(down, excluded))
	return Ratio{Up: o - d, Den: o}, Ratio{Up: o - m - (d - dm), Den: o - m}
}

// normalize sorts spans, ends open ones (zero To) at now, drops empty ones
// and merges those that touch or overlap.
func normalize(spans []Span, now time.Time) []Span {
	out := make([]Span, 0, len(spans))
	for _, s := range spans {
		if s.To.IsZero() {
			s.To = now
		}
		s = Span{s.From.Truncate(time.Second), s.To.Truncate(time.Second)}
		if s.To.After(s.From) {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b Span) int { return a.From.Compare(b.From) })
	merged := out[:0]
	for _, s := range out {
		if n := len(merged); n > 0 && !s.From.After(merged[n-1].To) {
			merged[n-1].To = latest(merged[n-1].To, s.To)
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

// intersect returns the parts of a within b; both normalized.
func intersect(a, b []Span) []Span {
	var out []Span
	for i, j := 0, 0; i < len(a) && j < len(b); {
		from, to := latest(a[i].From, b[j].From), earliest(a[i].To, b[j].To)
		if to.After(from) {
			out = append(out, Span{from, to})
		}
		if a[i].To.Before(b[j].To) {
			i++
		} else {
			j++
		}
	}
	return out
}

// subtract returns the parts of a outside b; both normalized.
func subtract(a, b []Span) []Span {
	var out []Span
	for _, s := range a {
		for _, cut := range b {
			if !cut.To.After(s.From) || !cut.From.Before(s.To) {
				continue
			}
			if cut.From.After(s.From) {
				out = append(out, Span{s.From, cut.From})
			}
			s.From = cut.To
			if !s.To.After(s.From) {
				break
			}
		}
		if s.To.After(s.From) {
			out = append(out, s)
		}
	}
	return out
}

func total(spans []Span) int64 {
	var sum int64
	for _, s := range spans {
		sum += int64(s.To.Sub(s.From) / time.Second)
	}
	return sum
}

// CeilSecond rounds t up to a whole second. A range ending now ends at the
// end of the current second, so it includes what was stored during it.
func CeilSecond(t time.Time) time.Time {
	if f := t.Truncate(time.Second); !f.Equal(t) {
		return f.Add(time.Second)
	}
	return t
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
