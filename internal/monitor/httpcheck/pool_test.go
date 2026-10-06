package httpcheck

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a settable clock for eviction tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func testPool(t *testing.T) (*Pool, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	p := NewPool("Sinjal/test")
	p.now = clk.now
	t.Cleanup(p.Close)
	return p, clk
}

func TestPoolReusesTransportPerKey(t *testing.T) {
	p, _ := testPool(t)
	a, _ := p.client(transportKey{}, true)
	b, _ := p.client(transportKey{}, false)
	if a.Transport != b.Transport {
		t.Error("the same key must share one transport, whatever the redirect policy")
	}
	for _, k := range []transportKey{{insecure: true}, {ipFamily: "ipv4"}, {proxy: "http://proxy:3128"}} {
		c, err := p.client(k, true)
		if err != nil {
			t.Fatal(err)
		}
		if c.Transport == a.Transport {
			t.Errorf("key %+v must get its own transport", k)
		}
	}
	if len(p.m) != 4 {
		t.Errorf("pool holds %d transports, want 4", len(p.m))
	}
	if _, err := p.client(transportKey{ipFamily: "ipv5"}, true); err == nil {
		t.Error("an unknown IP family must be an error")
	}
}

func TestPoolReusesConnections(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	p, _ := testPool(t)
	for range 5 {
		c, _ := p.client(transportKey{}, true)
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if n := conns.Load(); n != 1 {
		t.Errorf("5 sequential checks opened %d connections, want 1", n)
	}
}

func TestPoolEvictsUnusedTransports(t *testing.T) {
	p, clk := testPool(t)
	old, _ := p.client(transportKey{insecure: true}, true)
	clk.add(5 * time.Minute)
	kept, _ := p.client(transportKey{}, true)
	clk.add(6 * time.Minute) // insecure unused for 11 min, plain for 6
	if c, _ := p.client(transportKey{}, true); c.Transport != kept.Transport {
		t.Error("a transport used within 10 minutes must be kept")
	}
	if _, ok := p.m[transportKey{insecure: true}]; ok {
		t.Error("a transport unused for 10 minutes must be evicted")
	}
	if c, _ := p.client(transportKey{insecure: true}, true); c.Transport == old.Transport {
		t.Error("an evicted key must get a fresh transport")
	}
}

func TestPoolSweepsAtMostOncePerMinute(t *testing.T) {
	p, clk := testPool(t)
	p.client(transportKey{insecure: true}, true)
	p.client(transportKey{}, true) // sweep ran at t0
	clk.add(evictAfter)
	p.mu.Lock()
	p.lastSweep = clk.now().Add(-30 * time.Second)
	p.mu.Unlock()
	p.client(transportKey{}, true)
	if _, ok := p.m[transportKey{insecure: true}]; !ok {
		t.Error("no sweep may run within a minute of the last one")
	}
}

// redirectServer redirects /n to /n-1 until /0, which answers 200.
func redirectServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Path[1:])
		if n > 0 {
			http.Redirect(w, r, "/"+strconv.Itoa(n-1), http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPoolRedirectPolicy(t *testing.T) {
	srv := redirectServer(t)
	p, _ := testPool(t)

	noFollow, _ := p.client(transportKey{}, false)
	resp, err := noFollow.Get(srv.URL + "/1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("without following, status = %d, want the 302", resp.StatusCode)
	}

	follow, _ := p.client(transportKey{}, true)
	resp, err = follow.Get(srv.URL + "/" + strconv.Itoa(MaxRedirects))
	if err != nil {
		t.Fatalf("%d redirects must be followed: %v", MaxRedirects, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if _, err := follow.Get(srv.URL + "/" + strconv.Itoa(MaxRedirects+1)); !errors.Is(err, ErrTooManyRedirects) {
		t.Errorf("%d redirects: err = %v, want ErrTooManyRedirects", MaxRedirects+1, err)
	}
}

func TestPoolIPFamily(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	p, _ := testPool(t)
	v4, _ := p.client(transportKey{ipFamily: "ipv4"}, true)
	resp, err := v4.Get(srv.URL)
	if err != nil {
		t.Fatalf("ipv4 to 127.0.0.1: %v", err)
	}
	resp.Body.Close()
	v6, _ := p.client(transportKey{ipFamily: "ipv6"}, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if resp, err := v6.Do(req); err == nil {
		resp.Body.Close()
		t.Error("ipv6 must not reach an IPv4 address")
	}
}

func TestPoolConcurrentUse(t *testing.T) {
	p, clk := testPool(t)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 50 {
				k := transportKey{insecure: (i+j)%2 == 0, ipFamily: []string{"", "ipv4", "ipv6"}[j%3]}
				if _, err := p.client(k, j%2 == 0); err != nil {
					t.Error(err)
				}
				if j%10 == 0 {
					clk.add(time.Minute)
				}
			}
		}()
	}
	wg.Wait()
}
