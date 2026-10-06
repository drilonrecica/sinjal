// Package icmpcheck is the ICMP monitor check (docs/06, decision P0-04):
// one echo request per check, IPv4 or IPv6, on an unprivileged datagram
// socket when the kernel allows it and on a raw socket otherwise. Without
// either the check fails with kind "permission" and says how to fix it.
package icmpcheck

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/drilonrecica/sinjal/internal/monitor/tcpcheck"
)

// Failure kinds (docs/30), stored in check_results.error_kind.
const (
	KindTimeout    = "timeout"
	KindDNS        = "dns"
	KindPermission = "permission"
	KindConnect    = "connect"
	KindUnknown    = "unknown"
)

// PermissionHint is the message of a "permission" failure.
const PermissionHint = "ICMP is not permitted for this process: allow unprivileged ping " +
	"(sysctl net.ipv4.ping_group_range) or grant the CAP_NET_RAW capability; see docs/16"

// Config is one ICMP check, built from a monitor's validated settings.
type Config struct {
	Host    string
	Timeout time.Duration
}

// Validate checks an ICMP monitor's target: an IP address or hostname
// (the TCP rules) and a positive timeout.
func (c Config) Validate() error {
	if err := tcpcheck.ValidateHost(c.Host); err != nil {
		return err
	}
	if c.Timeout <= 0 {
		return errors.New("timeout must be greater than zero")
	}
	return nil
}

// Result is the outcome of one check. Duration is the round-trip time for
// a successful check and the time to the failure otherwise.
type Result struct {
	Started  time.Time
	Finished time.Time
	Duration time.Duration
	Success  bool
	Kind     string // "" on success
	Message  string
	Addr     netip.Addr // the address pinged; invalid when resolution failed
}

// conn is what a check needs of a socket; *icmp.PacketConn has it.
type conn interface {
	WriteTo(b []byte, dst net.Addr) (int, error)
	ReadFrom(b []byte) (int, net.Addr, error)
	SetDeadline(t time.Time) error
	Close() error
}

// mode is the kind of socket that works for an address family.
type mode uint8

const (
	modeUnknown mode = iota
	modeDatagram
	modeRaw
	modeDenied
)

// Pinger runs ICMP checks. It remembers per address family which socket
// works, so only the first check pays for a refused attempt; a permission
// change is picked up on restart. Safe for concurrent use.
type Pinger struct {
	mu    sync.Mutex
	modes [2]mode // index 0 IPv4, 1 IPv6

	listen func(network, address string) (conn, error)
}

// New returns a Pinger using real sockets.
func New() *Pinger {
	return &Pinger{listen: func(network, address string) (conn, error) {
		return icmp.ListenPacket(network, address)
	}}
}

// family describes the sockets and messages of one address family.
type family struct {
	index              int
	datagram, raw, any string
	proto              int
	request, reply     icmp.Type
}

var (
	family4 = family{0, "udp4", "ip4:icmp", "0.0.0.0", 1, ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply}
	family6 = family{1, "udp6", "ip6:ipv6-icmp", "::", 58, ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply}
)

// Check sends one echo request and waits for its reply. It never returns
// an error: every failure is a Result with a kind.
func (p *Pinger) Check(ctx context.Context, cfg Config) Result {
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	res := Result{Started: time.Now()}
	// fail ends the check. One cut off by the caller (shutdown) is
	// "unknown": not a verdict on the target.
	fail := func(kind, msg string) Result {
		if errors.Is(ctx.Err(), context.Canceled) {
			kind, msg = KindUnknown, "check cancelled"
		}
		return finish(res, kind, msg)
	}
	addr, err := resolve(ctx, cfg.Host)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fail(KindTimeout, fmt.Sprintf("%s did not resolve within %s", cfg.Host, cfg.Timeout))
	case err != nil:
		return fail(KindDNS, err.Error())
	}
	res.Addr = addr
	fam := family4
	if addr.Is6() {
		fam = family6
	}
	c, m, err := p.open(fam)
	if err != nil {
		if m == modeDenied {
			return fail(KindPermission, PermissionHint)
		}
		return fail(KindConnect, err.Error())
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	// A cancelled check (shutdown) must not wait for its deadline.
	stop := context.AfterFunc(ctx, func() { c.SetDeadline(time.Now()) })
	defer stop()

	var dst net.Addr = &net.IPAddr{IP: addr.AsSlice()}
	if m == modeDatagram {
		dst = &net.UDPAddr{IP: addr.AsSlice()}
	}
	req, nonce, seq, err := echoRequest(fam)
	if err != nil {
		return fail(KindUnknown, err.Error())
	}
	sent := time.Now()
	if _, err := c.WriteTo(req, dst); err != nil {
		return fail(KindConnect, err.Error())
	}
	buf := make([]byte, 1500)
	for {
		n, from, err := c.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) || ctx.Err() != nil {
				return fail(KindTimeout, fmt.Sprintf("no reply within %s", cfg.Timeout))
			}
			return fail(KindConnect, err.Error())
		}
		if isReply(fam, buf[:n], from, addr, seq, nonce) {
			// The round trip is measured from the send, not from the start
			// of the check, which includes resolution and the socket.
			rtt := time.Since(sent)
			res = finish(res, "", "")
			res.Success, res.Duration = true, rtt
			return res
		}
	}
}

// open returns a socket for fam, trying the cached mode or else datagram
// then raw. The mode is denied when both are refused for lack of permission.
func (p *Pinger) open(fam family) (conn, mode, error) {
	p.mu.Lock()
	m := p.modes[fam.index]
	p.mu.Unlock()
	switch m {
	case modeDenied:
		return nil, modeDenied, fs.ErrPermission
	case modeDatagram:
		c, err := p.listen(fam.datagram, fam.any)
		return c, modeDatagram, err
	case modeRaw:
		c, err := p.listen(fam.raw, fam.any)
		return c, modeRaw, err
	}
	c, err := p.listen(fam.datagram, fam.any)
	if err == nil {
		p.remember(fam, modeDatagram)
		return c, modeDatagram, nil
	}
	if !errors.Is(err, fs.ErrPermission) {
		return nil, modeUnknown, err
	}
	c, err = p.listen(fam.raw, fam.any)
	switch {
	case err == nil:
		p.remember(fam, modeRaw)
		return c, modeRaw, nil
	case errors.Is(err, fs.ErrPermission):
		p.remember(fam, modeDenied)
		return nil, modeDenied, err
	default:
		return nil, modeUnknown, err
	}
}

func (p *Pinger) remember(fam family, m mode) {
	p.mu.Lock()
	p.modes[fam.index] = m
	p.mu.Unlock()
}

// resolve returns the address to ping: the host itself if it is an IP
// address, else the first address it resolves to.
func resolve(ctx context.Context, host string) (netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return a.Unmap(), nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(addrs) == 0 {
		return netip.Addr{}, fmt.Errorf("no address for %s", host)
	}
	return addrs[0].Unmap(), nil
}

// echoRequest builds an echo request with a random sequence number and a
// random payload. In datagram mode the kernel replaces the identifier, so
// the reply is recognized by sequence number and payload instead.
func echoRequest(fam family) (msg, nonce []byte, seq int, err error) {
	var r [20]byte
	if _, err := rand.Read(r[:]); err != nil {
		return nil, nil, 0, err
	}
	nonce = r[4:]
	seq = int(binary.BigEndian.Uint16(r[2:4]))
	m := icmp.Message{Type: fam.request, Body: &icmp.Echo{
		ID:   int(binary.BigEndian.Uint16(r[:2])),
		Seq:  seq,
		Data: nonce,
	}}
	msg, err = m.Marshal(nil)
	return msg, nonce, seq, err
}

// isReply reports whether b is the reply to this check's request.
func isReply(fam family, b []byte, from net.Addr, target netip.Addr, seq int, nonce []byte) bool {
	m, err := icmp.ParseMessage(fam.proto, b)
	if err != nil || m.Type != fam.reply {
		return false
	}
	echo, ok := m.Body.(*icmp.Echo)
	if !ok || echo.Seq != seq || !bytes.Equal(echo.Data, nonce) {
		return false
	}
	return sourceIP(from) == target
}

func sourceIP(a net.Addr) netip.Addr {
	var ip net.IP
	switch a := a.(type) {
	case *net.UDPAddr:
		ip = a.IP
	case *net.IPAddr:
		ip = a.IP
	}
	addr, _ := netip.AddrFromSlice(ip)
	return addr.Unmap()
}

func finish(r Result, kind, msg string) Result {
	r.Finished = time.Now()
	r.Duration = r.Finished.Sub(r.Started)
	r.Kind, r.Message = kind, msg
	return r
}
