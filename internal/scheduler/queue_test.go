package scheduler

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

const s = time.Second

// drain pops everything due at now, as "id" or "id!" for a retry.
func drain(q *queue, now time.Time) string {
	out := ""
	for {
		j, ok := q.popDue(now)
		if !ok {
			return out
		}
		out += j.MonitorID
		if j.Retry {
			out += "!"
		}
		out += " "
	}
}

func TestQueueOrdersByNextRun(t *testing.T) {
	var q queue
	q.set(t0, "c", 60*s, 3*s)
	q.set(t0, "a", 60*s, 1*s)
	q.set(t0, "b", 60*s, 2*s)
	if got := drain(&q, t0); got != "" {
		t.Fatalf("nothing is due yet, got %q", got)
	}
	if next, ok := q.next(); !ok || !next.Equal(t0.Add(1*s)) {
		t.Fatalf("next = %v %v", next, ok)
	}
	if got := drain(&q, t0.Add(2*s)); got != "a b " {
		t.Fatalf("got %q", got)
	}
	if got := drain(&q, t0.Add(3*s)); got != "c " {
		t.Fatalf("got %q", got)
	}
	if q.Len() != 3 {
		t.Fatalf("monitors stay scheduled, len = %d", q.Len())
	}
}

func TestQueueCadenceIsFixed(t *testing.T) {
	var q queue
	const iv = 30 * s
	q.set(t0, "m", iv, 0)
	j, ok := q.popDue(t0)
	if !ok || !j.Due.Equal(t0) || j.Retry {
		t.Fatalf("first run: %+v %v", j, ok)
	}
	// The jitter offset is added once, after the first run; from then on
	// runs are exactly one interval apart, however late each one is popped.
	want := t0.Add(iv + jitter("m", iv))
	for i := range 5 {
		next, _ := q.next()
		if !next.Equal(want) {
			t.Fatalf("run %d at %v, want %v", i+2, next, want)
		}
		if _, ok := q.popDue(want.Add(-time.Nanosecond)); ok {
			t.Fatal("popped before it was due")
		}
		if j, ok := q.popDue(want.Add(700 * time.Millisecond)); !ok || !j.Due.Equal(want) {
			t.Fatalf("run %d: %+v %v", i+2, j, ok)
		}
		want = want.Add(iv)
	}
}

func TestQueueSetReplacesSchedule(t *testing.T) {
	var q queue
	q.set(t0, "m", 60*s, 0)
	drain(&q, t0)
	// An edit 10 s later: one entry, checked at once, new interval after.
	now := t0.Add(10 * s)
	q.set(now, "m", 20*s, 0)
	if q.Len() != 1 {
		t.Fatalf("len = %d", q.Len())
	}
	if got := drain(&q, now); got != "m " {
		t.Fatalf("got %q", got)
	}
	if next, _ := q.next(); !next.Equal(now.Add(20*s + jitter("m", 20*s))) {
		t.Fatalf("next = %v", next)
	}
}

func TestQueueRemove(t *testing.T) {
	var q queue
	for _, id := range []string{"a", "b", "c", "d"} {
		q.set(t0, id, 60*s, 0)
	}
	q.remove("b")
	q.remove("b")
	q.remove("nope")
	if got := drain(&q, t0); len(got) != len("a c d ") || q.Len() != 3 {
		t.Fatalf("got %q, len %d", got, q.Len())
	}
	q.remove("a")
	q.remove("c")
	q.remove("d")
	if _, ok := q.next(); ok || len(q.byID) != 0 {
		t.Fatal("queue should be empty")
	}
}

func TestQueueSkipsMissedRuns(t *testing.T) {
	var q queue
	q.set(t0, "m", 30*s, 0)
	drain(&q, t0)
	// Ten minutes of stall: one run, not twenty, then a full interval.
	late := t0.Add(10 * time.Minute)
	if got := drain(&q, late); got != "m " {
		t.Fatalf("got %q", got)
	}
	if next, _ := q.next(); !next.Equal(late.Add(30 * s)) {
		t.Fatalf("next = %v, want one interval after the late run", next)
	}
}

func TestQueueRetry(t *testing.T) {
	var q queue
	q.set(t0, "m", 30*s, 0)
	drain(&q, t0)
	regular, _ := q.next()

	q.extraAt("m", t0.Add(5*s))
	q.extraAt("m", t0.Add(8*s)) // a later request does not push it back
	if next, _ := q.next(); !next.Equal(t0.Add(5 * s)) {
		t.Fatalf("retry at %v", next)
	}
	if j, ok := q.popDue(t0.Add(5 * s)); !ok || !j.Retry || !j.Due.Equal(t0.Add(5*s)) {
		t.Fatalf("retry job: %+v %v", j, ok)
	}
	// The regular cadence is untouched by the retry.
	if next, _ := q.next(); !next.Equal(regular) {
		t.Fatalf("regular run moved from %v to %v", regular, next)
	}
	// A retry at or after the regular run adds nothing.
	q.extraAt("m", regular)
	q.extraAt("m", regular.Add(s))
	if j, _ := q.popDue(regular.Add(2 * s)); j.Retry {
		t.Fatal("the regular run should serve as the confirmation")
	}
	if got := drain(&q, regular.Add(2*s)); got != "" {
		t.Fatalf("extra run scheduled: %q", got)
	}
	q.extraAt("unknown", t0)
}

func TestQueueRetryGoesFirstOnATie(t *testing.T) {
	for _, order := range [][]string{{"a", "b", "r"}, {"r", "a", "b"}, {"a", "r", "b"}} {
		var q queue
		for _, id := range order {
			if id == "r" {
				q.set(t0, id, 60*s, 30*s)
				q.extraAt(id, t0.Add(10*s))
			} else {
				q.set(t0, id, 60*s, 10*s)
			}
		}
		if got := drain(&q, t0.Add(10*s)); got[:3] != "r! " {
			t.Errorf("insert order %v: got %q, retry should be first", order, got)
		}
	}
}

func TestQueueClampsBadInput(t *testing.T) {
	var q queue
	q.set(t0, "m", 0, -5*s)
	if got := drain(&q, t0); got != "m " {
		t.Fatalf("negative delay should mean now, got %q", got)
	}
	if got := drain(&q, t0); got != "" {
		t.Fatal("a zero interval must not leave the monitor due forever")
	}
}

// The heap invariant holds under random edits: jobs always come out in
// time order and every scheduled monitor is found again.
func TestQueueRandomOperations(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var q queue
	now := t0
	for range 20000 {
		id := fmt.Sprint(rng.Intn(200))
		switch rng.Intn(6) {
		case 0, 1:
			q.set(now, id, time.Duration(10+rng.Intn(120))*s, time.Duration(rng.Intn(30))*s)
		case 2:
			q.remove(id)
		case 3:
			q.extraAt(id, now.Add(time.Duration(rng.Intn(10))*s))
		default:
			now = now.Add(time.Duration(rng.Intn(3000)) * time.Millisecond)
			last := time.Time{}
			for {
				j, ok := q.popDue(now)
				if !ok {
					break
				}
				if j.Due.Before(last) || j.Due.After(now) {
					t.Fatalf("job due %v popped at %v after one due %v", j.Due, now, last)
				}
				last = j.Due
			}
		}
		if q.Len() != len(q.byID) {
			t.Fatalf("heap has %d items, index has %d", q.Len(), len(q.byID))
		}
		for i, it := range q.items {
			if it.index != i || q.byID[it.id] != it || it.at.After(it.regular) {
				t.Fatalf("broken item %+v at %d", it, i)
			}
		}
	}
}

func TestJitter(t *testing.T) {
	limits := map[time.Duration]time.Duration{
		10 * s:          1 * s,
		30 * s:          3 * s,
		60 * s:          6 * s,
		5 * time.Minute: 30 * s,
		time.Hour:       30 * s,
		24 * time.Hour:  30 * s,
	}
	for interval, limit := range limits {
		if got := maxJitter(interval); got != limit {
			t.Errorf("maxJitter(%v) = %v, want %v", interval, got, limit)
		}
		seen := map[time.Duration]bool{}
		for i := range 1000 {
			id := fmt.Sprintf("monitor-%d", i)
			j := jitter(id, interval)
			if j < 0 || j > limit {
				t.Fatalf("jitter(%q, %v) = %v, outside [0, %v]", id, interval, j, limit)
			}
			if j != jitter(id, interval) {
				t.Fatal("jitter is not deterministic")
			}
			seen[j.Truncate(limit/10)] = true
		}
		if len(seen) < 10 {
			t.Errorf("interval %v: offsets cover only %d of 10 slices of the window", interval, len(seen))
		}
	}
	if jitter("m", 0) != 0 || jitter("m", 5) != 0 {
		t.Error("tiny intervals get no jitter")
	}
}
