package recent

import (
	"runtime"
	"strconv"
	"testing"
)

// full returns a store of n monitors with full rings, and their ids.
func full(n int) (*Store, []string) {
	s := New()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "monitor-" + strconv.Itoa(i)
		for j := range Cap {
			s.Push(ids[i], Check{At: int64(j), MS: 120, OK: j%17 != 0})
		}
	}
	return s, ids
}

// BenchmarkPush is the processor's cost per stored result at 1,000 monitors.
func BenchmarkPush(b *testing.B) {
	s, ids := full(1000)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		s.Push(ids[i%len(ids)], Check{At: int64(i), MS: 80, OK: true})
		i++
	}
}

// BenchmarkNewestList is what the monitor list reads: 36 checks of each of
// 1,000 monitors into one reused buffer.
func BenchmarkNewestList(b *testing.B) {
	s, ids := full(1000)
	buf := make([]Check, 0, Cap)
	b.ReportAllocs()
	for b.Loop() {
		for _, id := range ids {
			buf = s.Newest(id, 36, buf[:0])
		}
	}
}

// TestMemoryAt1000 reports what the rings of 1,000 monitors take.
func TestMemoryAt1000(t *testing.T) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	s, _ := full(1000)
	runtime.GC()
	runtime.ReadMemStats(&after)
	mb := float64(after.HeapAlloc-before.HeapAlloc) / (1 << 20)
	t.Logf("1,000 full rings: %.1f MiB", mb)
	if mb > 6 {
		t.Fatalf("1,000 full rings take %.1f MiB, want at most 6 (docs/18 idle RAM)", mb)
	}
	runtime.KeepAlive(s)
}
