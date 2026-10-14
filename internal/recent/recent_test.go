package recent

import (
	"strconv"
	"sync"
	"testing"
	"unsafe"
)

func at(n int) Check { return Check{At: int64(n), MS: int32(n), OK: n%2 == 0} }

func ats(cs []Check) []int64 {
	out := make([]int64, len(cs))
	for i, c := range cs {
		out[i] = c.At
	}
	return out
}

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNewestIsNewestFirst(t *testing.T) {
	s := New()
	for i := 1; i <= 5; i++ {
		s.Push("m", at(i))
	}
	if got := ats(s.Newest("m", 3, nil)); !equal(got, []int64{5, 4, 3}) {
		t.Fatalf("newest 3 = %v, want [5 4 3]", got)
	}
	if got := ats(s.Newest("m", 10, nil)); !equal(got, []int64{5, 4, 3, 2, 1}) {
		t.Fatalf("newest 10 of 5 = %v, want all five", got)
	}
}

func TestRingKeepsTheLastCap(t *testing.T) {
	s := New()
	for i := 1; i <= Cap+7; i++ {
		s.Push("m", at(i))
	}
	got := s.Newest("m", Cap+50, nil)
	if len(got) != Cap {
		t.Fatalf("len = %d, want %d", len(got), Cap)
	}
	if got[0].At != Cap+7 || got[Cap-1].At != 8 {
		t.Fatalf("range = %d..%d, want %d..8", got[0].At, got[Cap-1].At, Cap+7)
	}
	if got[0] != at(Cap+7) {
		t.Fatalf("fields not kept: %+v", got[0])
	}
}

func TestUnknownMonitorHasNoChecks(t *testing.T) {
	if got := New().Newest("nope", 10, nil); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func TestSeedThenPush(t *testing.T) {
	s := New()
	s.Seed("m", []Check{at(1), at(2), at(3)})
	s.Push("m", at(4))
	if got := ats(s.Newest("m", 10, nil)); !equal(got, []int64{4, 3, 2, 1}) {
		t.Fatalf("got %v, want [4 3 2 1]", got)
	}
}

func TestSeedKeepsOnlyTheNewest(t *testing.T) {
	s := New()
	var cs []Check
	for i := 1; i <= Cap+3; i++ {
		cs = append(cs, at(i))
	}
	s.Seed("m", cs)
	got := s.Newest("m", Cap, nil)
	if len(got) != Cap || got[0].At != Cap+3 || got[Cap-1].At != 4 {
		t.Fatalf("got %d checks %d..%d", len(got), got[0].At, got[len(got)-1].At)
	}
}

func TestDrop(t *testing.T) {
	s := New()
	s.Push("m", at(1))
	s.Push("other", at(2))
	s.Drop("m")
	if got := s.Newest("m", 5, nil); len(got) != 0 {
		t.Fatalf("dropped monitor still has %v", got)
	}
	if got := s.Newest("other", 5, nil); len(got) != 1 {
		t.Fatalf("other monitor lost its checks: %v", got)
	}
}

func TestNewestReusesDst(t *testing.T) {
	s := New()
	s.Push("m", at(1))
	s.Push("m", at(2))
	buf := make([]Check, 0, 8)
	got := s.Newest("m", 8, buf)
	if &got[:1][0] != &buf[:1][0] {
		t.Fatal("Newest allocated although dst had room")
	}
	if allocs := testing.AllocsPerRun(100, func() { buf = s.Newest("m", 8, buf[:0]) }); allocs != 0 {
		t.Fatalf("%v allocations per read, want 0", allocs)
	}
}

func TestConcurrentPushAndRead(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 2000 {
			s.Push("m"+strconv.Itoa(i%10), at(i))
		}
	})
	for range 4 {
		wg.Go(func() {
			buf := make([]Check, 0, Cap)
			for i := range 2000 {
				buf = s.Newest("m"+strconv.Itoa(i%10), Cap, buf[:0])
			}
		})
	}
	wg.Wait()
}

// The engine seeds in the background while checks already arrive: seeded
// checks go before the live ones, and those the live ones already cover
// (stored after the seed's read began) are not repeated.
func TestSeedAfterLivePushes(t *testing.T) {
	s := New()
	s.Push("m", at(5))
	s.Push("m", at(6))
	s.Seed("m", []Check{at(2), at(3), at(4), at(5)})
	if got := ats(s.Newest("m", 10, nil)); !equal(got, []int64{6, 5, 4, 3, 2}) {
		t.Fatalf("got %v, want [6 5 4 3 2]", got)
	}
}

func TestSeedAfterAFullRingChangesNothing(t *testing.T) {
	s := New()
	for i := 100; i < 100+Cap; i++ {
		s.Push("m", at(i))
	}
	s.Seed("m", []Check{at(1), at(2)})
	got := s.Newest("m", Cap, nil)
	if len(got) != Cap || got[Cap-1].At != 100 {
		t.Fatalf("oldest = %d, want 100", got[Cap-1].At)
	}
}

// The Live activity column reads the newest checks of all monitors.
func TestFeed(t *testing.T) {
	s := New()
	for i := 1; i <= FeedCap+5; i++ {
		s.Push("m"+strconv.Itoa(i%3), at(i))
	}
	got := s.Feed(nil)
	if len(got) != FeedCap || got[0].Check.At != FeedCap+5 || got[FeedCap-1].Check.At != 6 {
		t.Fatalf("%d entries, %d..%d", len(got), got[0].Check.At, got[len(got)-1].Check.At)
	}
	if got[0].MonitorID != "m"+strconv.Itoa((FeedCap+5)%3) {
		t.Fatalf("monitor %q", got[0].MonitorID)
	}
	// Seeding fills the rings, not the feed: it is history, not activity.
	s2 := New()
	s2.Seed("m", []Check{at(1), at(2)})
	if len(s2.Feed(nil)) != 0 {
		t.Fatal("seeded checks are in the feed")
	}
	// A deleted monitor leaves the feed.
	s.Drop("m1")
	for _, e := range s.Feed(nil) {
		if e.MonitorID == "m1" {
			t.Fatal("a dropped monitor is still in the feed")
		}
	}
}

// The status code sits in the padding after OK: a ring of Cap checks per
// monitor costs no more memory than before it existed (docs/18).
func TestCheckSize(t *testing.T) {
	if got := unsafe.Sizeof(Check{}); got != 32 {
		t.Fatalf("Check is %d bytes, want 32", got)
	}
}
