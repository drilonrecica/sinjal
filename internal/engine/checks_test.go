package engine

import (
	"context"
	"net"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/drilonrecica/sinjal/internal/monitor/icmpcheck"
	"github.com/drilonrecica/sinjal/internal/store"
)

// typed creates an enabled monitor of in.Type with a 20 ms retry delay.
func (e *env) typed(in store.MonitorInput) string {
	e.t.Helper()
	in.Enabled, in.RetryDelayMS = true, 20
	if in.Name == "" {
		in.Name = in.Type
	}
	id, err := store.CreateMonitor(context.Background(), e.d, in, created)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestTCPMonitor(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	open := ln.Addr().(*net.TCPAddr).Port
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	closed.Close()

	up := e.typed(store.MonitorInput{Type: store.TypeTCP, Name: "open", TCP: store.TCPConfig{Host: "127.0.0.1", Port: open}})
	down := e.typed(store.MonitorInput{Type: store.TypeTCP, Name: "closed", TCP: store.TCPConfig{Host: "127.0.0.1", Port: closedPort}})
	e.start()

	eventually(t, "tcp up", func() bool { return e.get(up).State == "up" })
	eventually(t, "tcp down", func() bool { return e.get(down).State == "down" })
	if _, kind, _ := e.lastResult(down); kind != "connect" {
		t.Errorf("closed port kind = %q", kind)
	}
}

// dnsServer answers every A query for example.com with 192.0.2.1 on a local
// UDP port and returns its address.
func dnsServer(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var req dnsmessage.Message
			if req.Unpack(buf[:n]) != nil || len(req.Questions) != 1 {
				continue
			}
			q := req.Questions[0]
			resp := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: req.ID, Response: true, RecursionDesired: req.RecursionDesired, RecursionAvailable: true},
				Questions: req.Questions,
			}
			if q.Type == dnsmessage.TypeA {
				resp.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.AResource{A: [4]byte{192, 0, 2, 1}},
				}}
			}
			out, err := resp.Pack()
			if err == nil {
				pc.WriteTo(out, from)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func TestDNSMonitor(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	addr := dnsServer(t)
	dns := func(name string, expected ...string) store.MonitorInput {
		return store.MonitorInput{Type: store.TypeDNS, Name: name,
			DNS: store.DNSConfig{Hostname: "example.com", QueryType: "A", Resolver: addr, Expected: expected}}
	}
	up := e.typed(dns("match", "192.0.2.1"))
	down := e.typed(dns("mismatch", "192.0.2.9"))
	e.start()

	eventually(t, "dns up", func() bool { return e.get(up).State == "up" })
	eventually(t, "dns down", func() bool { return e.get(down).State == "down" })
	var kind, snippet string
	if err := e.d.Reader.QueryRow(`SELECT error_kind, diagnostic_snippet FROM check_results
		WHERE monitor_id = ? ORDER BY id DESC LIMIT 1`, down).Scan(&kind, &snippet); err != nil {
		t.Fatal(err)
	}
	if kind != "dns_mismatch" || !strings.Contains(snippet, "192.0.2.9") {
		t.Errorf("mismatch result = %q %q", kind, snippet)
	}
}

// TestICMPMonitor pings the loopback address. Where the process may not
// ping, the failure must be the explicit permission kind with its hint.
func TestICMPMonitor(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.typed(store.MonitorInput{Type: store.TypeICMP, ICMP: store.ICMPConfig{Host: "127.0.0.1"}})
	e.start()

	eventually(t, "icmp checked", func() bool { return e.rows(id) > 0 })
	ok, kind, msg := e.lastResult(id)
	switch {
	case ok:
	case kind == icmpcheck.KindPermission && msg == icmpcheck.PermissionHint:
		t.Log("ICMP not permitted here; the permission failure is surfaced")
	default:
		t.Errorf("loopback ping = %v %q %q", ok, kind, msg)
	}
}

func TestUnknownTypeIsUnusable(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.typed(store.MonitorInput{Type: store.TypeTCP, TCP: store.TCPConfig{Host: "127.0.0.1", Port: 1}})
	e.exec(`PRAGMA ignore_check_constraints = ON`)
	e.exec(`UPDATE monitors SET type = 'smtp' WHERE id = ?`, id)
	e.start()

	eventually(t, "checked", func() bool { return e.rows(id) > 0 })
	if ok, kind, msg := e.lastResult(id); ok || kind != "unknown" || !strings.Contains(msg, "unknown monitor type smtp") {
		t.Errorf("result = %v %q %q", ok, kind, msg)
	}
}
