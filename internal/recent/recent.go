// Package recent keeps the last checks of every monitor in memory for the
// beat bars (docs/04 "beat bar"). Reading them from SQLite for a list of
// 1,000 monitors costs the list's whole 50 ms budget (docs/18); a copy out
// of a ring costs microseconds. The result processor pushes every stored
// check; the engine seeds the rings from the database at start.
package recent

import "sync"

// Cap is how many checks a monitor keeps: the widest beat bar.
const Cap = 120

// FeedCap is how many checks of all monitors the feed keeps: the Live
// activity column (docs/03, from 2200 px).
const FeedCap = 50

// Entry is one check in the feed, with its monitor.
type Entry struct {
	MonitorID string
	Check     Check
}

// Check is one check as a beat bar shows it.
type Check struct {
	At     int64  // when the check started, Unix seconds
	MS     int32  // duration in milliseconds
	OK     bool   // the check succeeded
	Status int16  // protocol status code such as 503; 0 when there is none
	Kind   string // the stored failure kind; "" on success
}

// ring holds up to Cap checks; next is where the next one goes.
type ring struct {
	buf  [Cap]Check
	next int
	n    int
}

func (r *ring) push(c Check) {
	r.buf[r.next] = c
	r.next = (r.next + 1) % Cap
	r.n = min(r.n+1, Cap)
}

// Store is safe for concurrent use: one writer (the processor) and any
// number of readers (page handlers).
type Store struct {
	mu    sync.RWMutex
	rings map[string]*ring
	feed  [FeedCap]Entry // the newest checks of all monitors
	next  int
	n     int
}

// New returns an empty store.
func New() *Store {
	return &Store{rings: make(map[string]*ring)}
}

// Push records a monitor's newest check.
func (s *Store) Push(id string, c Check) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rings[id]
	if r == nil {
		r = &ring{}
		s.rings[id] = r
	}
	r.push(c)
	s.feed[s.next] = Entry{MonitorID: id, Check: c}
	s.next = (s.next + 1) % FeedCap
	s.n = min(s.n+1, FeedCap)
}

// Seed records checks read from the database, oldest first, before any
// pushed since: the engine seeds in the background while checks already
// arrive. Seeded checks from the second of the oldest pushed one onwards
// are already there and are left out. Only the newest Cap are kept.
func (s *Store) Seed(id string, cs []Check) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rings[id]
	if r == nil {
		r = &ring{}
		s.rings[id] = r
	}
	live := make([]Check, 0, r.n)
	for i := r.n - 1; i >= 0; i-- {
		live = append(live, r.buf[(r.next-1-i+Cap)%Cap])
	}
	if len(live) > 0 {
		n := 0
		for n < len(cs) && cs[n].At < live[0].At {
			n++
		}
		cs = cs[:n]
	}
	*r = ring{}
	for _, c := range cs[max(0, len(cs)+len(live)-Cap):] {
		r.push(c)
	}
	for _, c := range live {
		r.push(c)
	}
}

// Drop forgets a monitor (it was deleted), in its ring and in the feed.
func (s *Store) Drop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rings, id)
	for i := range s.feed {
		if s.feed[i].MonitorID == id {
			s.feed[i] = Entry{}
		}
	}
}

// Feed appends the newest checks of all monitors to dst, newest first:
// what was pushed (stored checks), not what was seeded at start.
func (s *Store) Feed(dst []Entry) []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.n {
		if e := s.feed[(s.next-1-i+FeedCap)%FeedCap]; e.MonitorID != "" {
			dst = append(dst, e)
		}
	}
	return dst
}

// Newest appends up to n of a monitor's checks to dst, newest first, and
// returns it. It does not allocate when dst has room.
func (s *Store) Newest(id string, n int, dst []Check) []Check {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.rings[id]
	if r == nil {
		return dst
	}
	for i := range min(n, r.n) {
		dst = append(dst, r.buf[(r.next-1-i+Cap)%Cap])
	}
	return dst
}
