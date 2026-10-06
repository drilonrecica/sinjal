// Package ratelimit is a small, memory-bounded failure counter for
// authentication endpoints (setup, login). It is not a general request
// limiter (docs/14_API.md: no heavy global rate-limit subsystem).
package ratelimit

import (
	"sync"
	"time"
)

// Limiter counts events per key in fixed windows. At most capacity keys are
// tracked; when full, the key whose window started first is evicted, so an
// attacker spraying keys cannot grow memory.
type Limiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	capacity int
	entries  map[string]*entry
}

type entry struct {
	start time.Time
	count int
}

// New allows max events per key within window, tracking at most capacity keys.
func New(max int, window time.Duration, capacity int) *Limiter {
	return &Limiter{max: max, window: window, capacity: capacity, entries: make(map[string]*entry, capacity)}
}

// Blocked reports whether key has used up its allowance at now.
func (l *Limiter) Blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	return ok && now.Sub(e.start) < l.window && e.count >= l.max
}

// Add records one event for key at now.
func (l *Limiter) Add(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[key]; ok {
		if now.Sub(e.start) >= l.window {
			e.start, e.count = now, 0
		}
		e.count++
		return
	}
	if len(l.entries) >= l.capacity {
		l.evict(now)
	}
	l.entries[key] = &entry{start: now, count: 1}
}

// Reset forgets key, for example after a successful login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// evict drops expired entries, or the oldest one if none has expired.
// O(capacity), only on insert into a full map.
func (l *Limiter) evict(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for k, e := range l.entries {
		if now.Sub(e.start) >= l.window {
			delete(l.entries, k)
			continue
		}
		if oldestKey == "" || e.start.Before(oldest) {
			oldestKey, oldest = k, e.start
		}
	}
	if len(l.entries) >= l.capacity {
		delete(l.entries, oldestKey)
	}
}
