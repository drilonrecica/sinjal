package icmpcheck

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/icmp"
)

func TestCheckLoopback(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1"} {
		t.Run(host, func(t *testing.T) {
			res := New().Check(context.Background(), Config{Host: host, Timeout: 2 * time.Second})
			if res.Kind == KindPermission {
				t.Skip("ICMP not permitted here: " + res.Message)
			}
			if res.Kind == KindConnect && host == "::1" {
				t.Skip("no IPv6 here: " + res.Message)
			}
			if !res.Success || res.Kind != "" || res.Message != "" {
				t.Fatalf("res = %+v", res)
			}
			if res.Duration <= 0 || res.Duration > time.Second || res.Finished.Before(res.Started) {
				t.Errorf("rtt = %s", res.Duration)
			}
			if res.Addr != netip.MustParseAddr(host) {
				t.Errorf("addr = %s", res.Addr)
			}
		})
	}
}

// fakeConn is a socket whose replies are made by respond from each request.
type fakeConn struct {
	mu       sync.Mutex
	deadline time.Time
	wake     chan struct{}
	replies  chan fakeReply
	respond  func(echo *icmp.Echo, dst net.Addr) []fakeReply
	closed   bool
	proto    int
}

type fakeReply struct {
	msg  icmp.Message
	from net.Addr
}

func newFakeConn(proto int, respond func(*icmp.Echo, net.Addr) []fakeReply) *fakeConn {
	return &fakeConn{wake: make(chan struct{}, 1), replies: make(chan fakeReply, 8), respond: respond, proto: proto}
}

func (c *fakeConn) WriteTo(b []byte, dst net.Addr) (int, error) {
	m, err := icmp.ParseMessage(c.proto, b)
	if err != nil {
		return 0, err
	}
	for _, r := range c.respond(m.Body.(*icmp.Echo), dst) {
		c.replies <- r
	}
	return len(b), nil
}

func (c *fakeConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		dl := c.deadline
		c.mu.Unlock()
		var timeout <-chan time.Time
		if !dl.IsZero() {
			timer := time.NewTimer(time.Until(dl))
			defer timer.Stop()
			timeout = timer.C
		}
		select {
		case r := <-c.replies:
			wire, err := r.msg.Marshal(nil)
			if err != nil {
				return 0, nil, err
			}
			return copy(b, wire), r.from, nil
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		case <-c.wake:
		}
	}
}

func (c *fakeConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

// fakePinger returns a Pinger whose listen answers per network: a socket
// from conns, or the error in errs. calls counts the listens.
func fakePinger(conns map[string]*fakeConn, errs map[string]error, calls map[string]int) *Pinger {
	return &Pinger{listen: func(network, _ string) (conn, error) {
		calls[network]++
		if err := errs[network]; err != nil {
			return nil, err
		}
		if c := conns[network]; c != nil {
			return c, nil
		}
		return nil, errors.New("unexpected network " + network)
	}}
}

func reply(echo *icmp.Echo, seq int, data []byte, from net.Addr) fakeReply {
	return fakeReply{
		msg:  icmp.Message{Type: family4.reply, Body: &icmp.Echo{ID: echo.ID + 1, Seq: seq, Data: data}},
		from: from,
	}
}

var target = &net.UDPAddr{IP: net.IPv4(192, 0, 2, 7)}

func TestMatchesSequenceAndNonceNotID(t *testing.T) {
	c := newFakeConn(1, func(e *icmp.Echo, _ net.Addr) []fakeReply {
		other := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 8)}
		return []fakeReply{
			reply(e, e.Seq+1, e.Data, target),                                 // wrong sequence
			reply(e, e.Seq, []byte("other probe"), target),                    // wrong payload
			reply(e, e.Seq, e.Data, other),                                    // wrong source
			{msg: icmp.Message{Type: family4.request, Body: e}, from: target}, // our own request
			reply(e, e.Seq, e.Data, target),                                   // the reply, with a rewritten ID
		}
	})
	p := fakePinger(map[string]*fakeConn{"udp4": c}, nil, map[string]int{})
	res := p.Check(context.Background(), Config{Host: "192.0.2.7", Timeout: time.Second})
	if !res.Success {
		t.Fatalf("res = %+v", res)
	}
	if !c.closed {
		t.Error("socket left open")
	}
	if len(c.replies) != 0 {
		t.Errorf("%d replies unread: matched too early", len(c.replies))
	}
}

func TestNoMatchingReplyTimesOut(t *testing.T) {
	c := newFakeConn(1, func(e *icmp.Echo, _ net.Addr) []fakeReply {
		return []fakeReply{reply(e, e.Seq, []byte("stale nonce 1234"), target)}
	})
	p := fakePinger(map[string]*fakeConn{"udp4": c}, nil, map[string]int{})
	start := time.Now()
	res := p.Check(context.Background(), Config{Host: "192.0.2.7", Timeout: 100 * time.Millisecond})
	if res.Success || res.Kind != KindTimeout || !strings.Contains(res.Message, "no reply within 100ms") {
		t.Fatalf("res = %+v", res)
	}
	if time.Since(start) > time.Second {
		t.Errorf("took %s", time.Since(start))
	}
	if !c.closed {
		t.Error("socket left open")
	}
}

func TestCancelledIsUnknown(t *testing.T) {
	c := newFakeConn(1, func(*icmp.Echo, net.Addr) []fakeReply { return nil })
	p := fakePinger(map[string]*fakeConn{"udp4": c}, nil, map[string]int{})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	res := p.Check(ctx, Config{Host: "192.0.2.7", Timeout: 10 * time.Second})
	if res.Kind != KindUnknown || res.Message != "check cancelled" {
		t.Fatalf("res = %+v", res)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("cancel took %s", time.Since(start))
	}
}

func permissionErr(network string) error {
	return &net.OpError{Op: "listen", Net: network, Err: os.NewSyscallError("socket", syscall.EPERM)}
}

func TestPermissionDenied(t *testing.T) {
	calls := map[string]int{}
	errs := map[string]error{
		"udp4":     &net.OpError{Op: "listen", Net: "udp4", Err: os.NewSyscallError("socket", syscall.EACCES)},
		"ip4:icmp": permissionErr("ip4:icmp"),
	}
	p := fakePinger(nil, errs, calls)
	for range 2 {
		res := p.Check(context.Background(), Config{Host: "192.0.2.7", Timeout: time.Second})
		if res.Success || res.Kind != KindPermission || res.Message != PermissionHint {
			t.Fatalf("res = %+v", res)
		}
	}
	for _, want := range []string{"ping_group_range", "CAP_NET_RAW", "docs/16"} {
		if !strings.Contains(PermissionHint, want) {
			t.Errorf("hint misses %q", want)
		}
	}
	// The denial is remembered: the second check opens no socket.
	if calls["udp4"] != 1 || calls["ip4:icmp"] != 1 {
		t.Errorf("listen calls = %v", calls)
	}
}

func TestRawFallback(t *testing.T) {
	calls := map[string]int{}
	raw := &net.IPAddr{IP: net.IPv4(192, 0, 2, 7)}
	var gotDst []net.Addr
	c := newFakeConn(1, func(e *icmp.Echo, dst net.Addr) []fakeReply {
		gotDst = append(gotDst, dst)
		return []fakeReply{reply(e, e.Seq, e.Data, raw)}
	})
	p := fakePinger(map[string]*fakeConn{"ip4:icmp": c}, map[string]error{"udp4": permissionErr("udp4")}, calls)
	for range 2 {
		if res := p.Check(context.Background(), Config{Host: "192.0.2.7", Timeout: time.Second}); !res.Success {
			t.Fatalf("res = %+v", res)
		}
	}
	if calls["udp4"] != 1 || calls["ip4:icmp"] != 2 {
		t.Errorf("listen calls = %v", calls)
	}
	if _, ok := gotDst[0].(*net.IPAddr); !ok {
		t.Errorf("raw socket sent to %T", gotDst[0])
	}
}

func TestOtherListenErrorIsConnectAndNotCached(t *testing.T) {
	calls := map[string]int{}
	errs := map[string]error{"udp6": &net.OpError{Op: "listen", Net: "udp6", Err: os.NewSyscallError("socket", syscall.EAFNOSUPPORT)}}
	p := fakePinger(nil, errs, calls)
	for range 2 {
		res := p.Check(context.Background(), Config{Host: "2001:db8::1", Timeout: time.Second})
		if res.Kind != KindConnect {
			t.Fatalf("res = %+v", res)
		}
	}
	if calls["udp6"] != 2 || calls["ip6:ipv6-icmp"] != 0 {
		t.Errorf("listen calls = %v", calls)
	}
}

func TestUnresolvableHost(t *testing.T) {
	res := New().Check(context.Background(), Config{Host: "nothing.invalid", Timeout: 2 * time.Second})
	if res.Success || (res.Kind != KindDNS && res.Kind != KindTimeout) || res.Addr.IsValid() {
		t.Fatalf("res = %+v", res)
	}
}

func TestPermissionErrorsMatch(t *testing.T) {
	// The classification relies on syscall errors matching fs.ErrPermission.
	for _, e := range []syscall.Errno{syscall.EPERM, syscall.EACCES} {
		if !errors.Is(permissionErrFor(e), fs.ErrPermission) {
			t.Errorf("%v does not match fs.ErrPermission", e)
		}
	}
}

func permissionErrFor(e syscall.Errno) error {
	return fmt.Errorf("listen: %w", &net.OpError{Op: "listen", Err: os.NewSyscallError("socket", e)})
}

func TestValidate(t *testing.T) {
	cases := []struct {
		cfg Config
		ok  bool
	}{
		{Config{Host: "example.com", Timeout: time.Second}, true},
		{Config{Host: "192.0.2.1", Timeout: time.Second}, true},
		{Config{Host: "2001:db8::1", Timeout: time.Second}, true},
		{Config{Host: "", Timeout: time.Second}, false},
		{Config{Host: "https://example.com", Timeout: time.Second}, false},
		{Config{Host: "example.com:80", Timeout: time.Second}, false},
		{Config{Host: "example.com", Timeout: 0}, false},
	}
	for _, c := range cases {
		if err := c.cfg.Validate(); (err == nil) != c.ok {
			t.Errorf("Validate(%+v) = %v", c.cfg, err)
		}
	}
}
