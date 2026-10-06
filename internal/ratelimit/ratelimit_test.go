package ratelimit

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestBlocksAfterMaxWithinWindow(t *testing.T) {
	l := New(3, time.Minute, 10)
	for i := range 3 {
		if l.Blocked("a", t0) {
			t.Fatalf("blocked after %d events", i)
		}
		l.Add("a", t0)
	}
	if !l.Blocked("a", t0.Add(59*time.Second)) {
		t.Error("not blocked after max events")
	}
	if l.Blocked("b", t0) {
		t.Error("other key blocked")
	}
	if l.Blocked("a", t0.Add(time.Minute)) {
		t.Error("still blocked after the window")
	}
	l.Add("a", t0.Add(time.Minute))
	if l.Blocked("a", t0.Add(time.Minute)) {
		t.Error("window did not restart")
	}
}

func TestReset(t *testing.T) {
	l := New(1, time.Minute, 10)
	l.Add("a", t0)
	l.Reset("a")
	if l.Blocked("a", t0) {
		t.Error("blocked after Reset")
	}
}

func TestCapacityIsBounded(t *testing.T) {
	l := New(1, time.Hour, 5)
	for i := range 100 {
		l.Add(strconv.Itoa(i), t0.Add(time.Duration(i)*time.Second))
	}
	if len(l.entries) != 5 {
		t.Fatalf("tracked %d keys, want 5", len(l.entries))
	}
	// The newest keys survive; the oldest were evicted.
	if !l.Blocked("99", t0.Add(100*time.Second)) || l.Blocked("0", t0.Add(100*time.Second)) {
		t.Error("eviction did not drop the oldest key")
	}
}

func TestEvictPrefersExpired(t *testing.T) {
	l := New(1, time.Minute, 2)
	l.Add("old", t0)
	l.Add("recent", t0.Add(90*time.Second))
	l.Add("new", t0.Add(100*time.Second)) // "old" has expired and goes first
	if !l.Blocked("recent", t0.Add(100*time.Second)) {
		t.Error("a live key was evicted while an expired one existed")
	}
}

func TestConcurrent(t *testing.T) {
	l := New(1000, time.Minute, 50)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 500 {
				k := strconv.Itoa(g*1000 + i%80)
				l.Add(k, t0)
				l.Blocked(k, t0)
			}
		})
	}
	wg.Wait()
	if len(l.entries) > 50 {
		t.Errorf("tracked %d keys, capacity 50", len(l.entries))
	}
}
