package dnscheck

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/net/dns/dnsmessage"
)

// handler answers one query; nil means no reply.
type handler func(req dnsmessage.Message, tcp bool) *dnsmessage.Message

// serve starts a DNS server on 127.0.0.1 (UDP and TCP on one port) and
// returns its address. tcpQueries counts queries over TCP.
func serve(t *testing.T, h handler) (addr string, tcpQueries *atomic.Int32) {
	t.Helper()
	var pc net.PacketConn
	var ln net.Listener
	for range 10 {
		var err error
		if pc, err = net.ListenPacket("udp", "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
		if ln, err = net.Listen("tcp", pc.LocalAddr().String()); err == nil {
			break
		}
		pc.Close()
		ln = nil
	}
	if ln == nil {
		t.Fatal("no port free for both UDP and TCP")
	}
	t.Cleanup(func() { pc.Close(); ln.Close() })
	tcpQueries = new(atomic.Int32)

	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var req dnsmessage.Message
			if req.Unpack(buf[:n]) != nil {
				continue
			}
			if m := h(req, false); m != nil {
				b, _ := m.Pack()
				pc.WriteTo(b, from)
			}
		}
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var size [2]byte
				if _, err := io.ReadFull(c, size[:]); err != nil {
					return
				}
				b := make([]byte, binary.BigEndian.Uint16(size[:]))
				if _, err := io.ReadFull(c, b); err != nil {
					return
				}
				var req dnsmessage.Message
				if req.Unpack(b) != nil {
					return
				}
				tcpQueries.Add(1)
				if m := h(req, true); m != nil {
					out, _ := m.Pack()
					c.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(out))), out...))
				}
			}()
		}
	}()
	return pc.LocalAddr().String(), tcpQueries
}

func answer(req dnsmessage.Message, rcode dnsmessage.RCode, rrs ...dnsmessage.Resource) *dnsmessage.Message {
	return &dnsmessage.Message{
		Header:    dnsmessage.Header{ID: req.ID, Response: true, RecursionAvailable: true, RCode: rcode},
		Questions: req.Questions,
		Answers:   rrs,
	}
}

// records answers every query with rrs, owned by the queried name.
func records(rrs ...dnsmessage.ResourceBody) handler {
	return func(req dnsmessage.Message, _ bool) *dnsmessage.Message {
		var out []dnsmessage.Resource
		for _, b := range rrs {
			out = append(out, rr(req.Questions[0].Name, b))
		}
		return answer(req, dnsmessage.RCodeSuccess, out...)
	}
}

func rr(name dnsmessage.Name, b dnsmessage.ResourceBody) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: name, Type: typeOf(b), Class: dnsmessage.ClassINET, TTL: 60},
		Body:   b,
	}
}

func typeOf(b dnsmessage.ResourceBody) dnsmessage.Type {
	switch b.(type) {
	case *dnsmessage.AResource:
		return dnsmessage.TypeA
	case *dnsmessage.AAAAResource:
		return dnsmessage.TypeAAAA
	case *dnsmessage.CNAMEResource:
		return dnsmessage.TypeCNAME
	case *dnsmessage.NSResource:
		return dnsmessage.TypeNS
	case *dnsmessage.MXResource:
		return dnsmessage.TypeMX
	case *dnsmessage.TXTResource:
		return dnsmessage.TypeTXT
	}
	panic("unsupported record")
}

func name(s string) dnsmessage.Name { return dnsmessage.MustNewName(s) }

func a(s string) *dnsmessage.AResource {
	return &dnsmessage.AResource{A: [4]byte(net.ParseIP(s).To4())}
}

func aaaa(s string) *dnsmessage.AAAAResource {
	return &dnsmessage.AAAAResource{AAAA: [16]byte(net.ParseIP(s).To16())}
}

func check(addr, qtype string, expected ...string) Config {
	return Config{Hostname: "example.test", QueryType: qtype, Resolver: addr, Expected: expected, Timeout: 2 * time.Second}
}

func TestQueryTypes(t *testing.T) {
	cases := []struct {
		qtype    string
		body     []dnsmessage.ResourceBody
		expected []string
		answers  []string
	}{
		{"A", []dnsmessage.ResourceBody{a("192.0.2.1"), a("192.0.2.2")}, []string{"192.0.2.2"}, []string{"192.0.2.1", "192.0.2.2"}},
		{"AAAA", []dnsmessage.ResourceBody{aaaa("2001:db8::1")}, []string{"2001:DB8:0::1"}, []string{"2001:db8::1"}},
		{"CNAME", []dnsmessage.ResourceBody{&dnsmessage.CNAMEResource{CNAME: name("Edge.CDN.test.")}}, []string{"edge.cdn.test."}, []string{"edge.cdn.test"}},
		{"NS", []dnsmessage.ResourceBody{&dnsmessage.NSResource{NS: name("ns1.example.test.")}, &dnsmessage.NSResource{NS: name("NS2.example.test.")}}, []string{"ns2.example.test", "ns1.EXAMPLE.test"}, []string{"ns1.example.test", "ns2.example.test"}},
		{"MX", []dnsmessage.ResourceBody{&dnsmessage.MXResource{Pref: 10, MX: name("mx1.example.test.")}, &dnsmessage.MXResource{Pref: 20, MX: name("mx2.example.test.")}}, []string{"mx2.example.test", "10 mx1.example.test"}, []string{"10 mx1.example.test", "20 mx2.example.test"}},
		{"TXT", []dnsmessage.ResourceBody{&dnsmessage.TXTResource{TXT: []string{"v=spf1 include:a.test ", "-all"}}}, []string{"v=spf1 include:a.test -all"}, []string{"v=spf1 include:a.test -all"}},
	}
	for _, c := range cases {
		t.Run(c.qtype, func(t *testing.T) {
			types := make(chan dnsmessage.Type, 1)
			addr, tcp := serve(t, func(req dnsmessage.Message, tcp bool) *dnsmessage.Message {
				types <- req.Questions[0].Type
				return records(c.body...)(req, tcp)
			})
			cfg := check(addr, c.qtype, c.expected...)
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			res := Check(context.Background(), cfg)
			if !res.Success || res.Kind != "" {
				t.Fatalf("res = %+v", res)
			}
			if !slices.Equal(res.Answers, c.answers) {
				t.Errorf("answers = %q, want %q", res.Answers, c.answers)
			}
			if gotType := <-types; gotType != queryTypes[c.qtype] || tcp.Load() != 0 || res.Server != addr {
				t.Errorf("query type %v, tcp %d, server %s", gotType, tcp.Load(), res.Server)
			}
		})
	}
}

func TestQueryIsFullyQualifiedWithRecursion(t *testing.T) {
	reqs := make(chan dnsmessage.Message, 1)
	addr, _ := serve(t, func(req dnsmessage.Message, tcp bool) *dnsmessage.Message {
		reqs <- req
		return records(a("192.0.2.1"))(req, tcp)
	})
	Check(context.Background(), check(addr, "A"))
	got := <-reqs
	if got.Questions[0].Name.String() != "example.test." || !got.RecursionDesired {
		t.Errorf("query = %+v", got)
	}
	if len(got.Additionals) != 1 || got.Additionals[0].Header.Type != dnsmessage.TypeOPT {
		t.Errorf("no EDNS0 OPT: %+v", got.Additionals)
	}
}

func TestAliasAnswersCollectTargetRecords(t *testing.T) {
	addr, _ := serve(t, func(req dnsmessage.Message, _ bool) *dnsmessage.Message {
		return answer(req, dnsmessage.RCodeSuccess,
			rr(req.Questions[0].Name, &dnsmessage.CNAMEResource{CNAME: name("target.test.")}),
			rr(name("target.test."), a("192.0.2.9")))
	})
	res := Check(context.Background(), check(addr, "A", "192.0.2.9"))
	if !res.Success || !slices.Equal(res.Answers, []string{"192.0.2.9"}) {
		t.Fatalf("res = %+v", res)
	}
}

func TestNXDomainAndNoAnswerDiffer(t *testing.T) {
	addr, _ := serve(t, func(req dnsmessage.Message, _ bool) *dnsmessage.Message {
		if strings.HasPrefix(req.Questions[0].Name.String(), "gone.") {
			return answer(req, dnsmessage.RCodeNameError)
		}
		// NOERROR with only a record of another type: NODATA for A.
		return answer(req, dnsmessage.RCodeSuccess, rr(req.Questions[0].Name, aaaa("2001:db8::1")))
	})
	cfg := check(addr, "A")
	cfg.Hostname = "gone.example.test"
	if res := Check(context.Background(), cfg); res.Kind != KindNXDomain || !strings.Contains(res.Message, "NXDOMAIN") {
		t.Errorf("nxdomain: %+v", res)
	}
	if res := Check(context.Background(), check(addr, "A")); res.Kind != KindNoAnswer || res.Message != "no A record for example.test" {
		t.Errorf("no answer: %+v", res)
	}
}

func TestErrorRcode(t *testing.T) {
	addr, _ := serve(t, func(req dnsmessage.Message, _ bool) *dnsmessage.Message {
		return answer(req, dnsmessage.RCodeServerFailure)
	})
	if res := Check(context.Background(), check(addr, "A")); res.Kind != KindError || res.Message != "resolver answered SERVFAIL" {
		t.Fatalf("res = %+v", res)
	}
}

func TestTruncatedRetriesOverTCP(t *testing.T) {
	addr, tcp := serve(t, func(req dnsmessage.Message, tcp bool) *dnsmessage.Message {
		if !tcp {
			m := answer(req, dnsmessage.RCodeSuccess)
			m.Truncated = true
			return m
		}
		return records(&dnsmessage.TXTResource{TXT: []string{strings.Repeat("x", 200)}})(req, tcp)
	})
	res := Check(context.Background(), check(addr, "TXT"))
	if !res.Success || tcp.Load() != 1 || len(res.Answers) != 1 {
		t.Fatalf("res = %+v, tcp = %d", res, tcp.Load())
	}
}

func TestWrongIDIgnored(t *testing.T) {
	addr, _ := serve(t, func(req dnsmessage.Message, tcp bool) *dnsmessage.Message {
		m := records(a("192.0.2.1"))(req, tcp)
		m.ID++
		return m
	})
	cfg := check(addr, "A")
	cfg.Timeout = 150 * time.Millisecond
	res := Check(context.Background(), cfg)
	if res.Kind != KindTimeout || res.Message != "no answer within 150ms" {
		t.Fatalf("res = %+v", res)
	}
}

func TestWrongQuestionIgnored(t *testing.T) {
	addr, _ := serve(t, func(req dnsmessage.Message, tcp bool) *dnsmessage.Message {
		m := records(a("192.0.2.1"))(req, tcp)
		m.Questions = []dnsmessage.Question{{Name: name("other.test."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}
		return m
	})
	cfg := check(addr, "A")
	cfg.Timeout = 150 * time.Millisecond
	if res := Check(context.Background(), cfg); res.Kind != KindTimeout {
		t.Fatalf("res = %+v", res)
	}
}

func TestSilentServerTimesOut(t *testing.T) {
	addr, _ := serve(t, func(dnsmessage.Message, bool) *dnsmessage.Message { return nil })
	cfg := check(addr, "A")
	cfg.Timeout = 100 * time.Millisecond
	start := time.Now()
	res := Check(context.Background(), cfg)
	if res.Kind != KindTimeout || time.Since(start) > time.Second {
		t.Fatalf("res = %+v after %s", res, time.Since(start))
	}
}

func TestCancelledIsUnknown(t *testing.T) {
	addr, _ := serve(t, func(dnsmessage.Message, bool) *dnsmessage.Message { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	cfg := check(addr, "A")
	cfg.Timeout = 10 * time.Second
	start := time.Now()
	res := Check(ctx, cfg)
	if res.Kind != KindUnknown || res.Message != "check cancelled" || time.Since(start) > 2*time.Second {
		t.Fatalf("res = %+v after %s", res, time.Since(start))
	}
}

func TestMatchModes(t *testing.T) {
	addr, _ := serve(t, records(a("192.0.2.1"), a("192.0.2.2")))
	cases := []struct {
		mode     string
		expected []string
		ok       bool
	}{
		{"", []string{"192.0.2.1", "192.0.2.2"}, true},
		{MatchAll, []string{"192.0.2.1"}, true}, // extra answers are allowed
		{MatchAll, []string{"192.0.2.1", "192.0.2.3"}, false},
		{MatchAny, []string{"192.0.2.3", "192.0.2.2"}, true},
		{MatchAny, []string{"192.0.2.3", "192.0.2.4"}, false},
	}
	for _, c := range cases {
		cfg := check(addr, "A", c.expected...)
		cfg.MatchMode = c.mode
		res := Check(context.Background(), cfg)
		if res.Success != c.ok {
			t.Errorf("%q %v: %+v", c.mode, c.expected, res)
		}
		if !c.ok && (res.Kind != KindMismatch || !strings.Contains(res.Snippet, "missing: 192.0.2.3") ||
			!strings.Contains(res.Snippet, "answer: 192.0.2.1, 192.0.2.2")) {
			t.Errorf("mismatch result: %+v", res)
		}
	}
}

func TestMXPreference(t *testing.T) {
	addr, _ := serve(t, records(&dnsmessage.MXResource{Pref: 10, MX: name("mx.example.test.")}))
	for exp, ok := range map[string]bool{
		"mx.example.test":     true,
		"MX.example.test.":    true,
		"10 mx.example.test":  true,
		"20 mx.example.test":  false,
		"10 mx2.example.test": false,
	} {
		if res := Check(context.Background(), check(addr, "MX", exp)); res.Success != ok {
			t.Errorf("%q: %+v", exp, res)
		}
	}
}

func TestTXTIsCaseSensitive(t *testing.T) {
	addr, _ := serve(t, records(&dnsmessage.TXTResource{TXT: []string{"Token=ABC"}}))
	if res := Check(context.Background(), check(addr, "TXT", "token=abc")); res.Kind != KindMismatch {
		t.Fatalf("res = %+v", res)
	}
}

func TestSnippetCapped(t *testing.T) {
	chunk := strings.Repeat("é", 100) // two bytes each; a TXT string holds up to 255
	long := strings.Repeat(chunk, 4)
	addr, _ := serve(t, records(&dnsmessage.TXTResource{TXT: []string{chunk, chunk, chunk, chunk}}, &dnsmessage.TXTResource{TXT: []string{"\xff\xfe"}}))
	res := Check(context.Background(), check(addr, "TXT", "x"+long))
	if res.Kind != KindMismatch || len(res.Snippet) > snippetCap || !utf8.ValidString(res.Snippet) || !strings.HasSuffix(res.Snippet, "…") {
		t.Fatalf("snippet %d bytes, valid %v", len(res.Snippet), utf8.ValidString(res.Snippet))
	}
}

func TestNextServerAfterUnreachable(t *testing.T) {
	good, _ := serve(t, records(a("192.0.2.1")))
	// A UDP port nobody listens on: the read fails with "connection refused".
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := pc.LocalAddr().String()
	pc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	q := dnsmessage.Question{Name: name("example.test."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}
	m, server, err := query(ctx, []string{dead, good}, q)
	if err != nil || server != good || len(m.Answers) != 1 {
		t.Fatalf("server %s, err %v", server, err)
	}
	_, _, err = query(ctx, []string{dead}, q)
	if err == nil || !strings.Contains(err.Error(), dead) {
		t.Errorf("err = %v", err)
	}
}

func TestUnreachableResolverIsDNSError(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := pc.LocalAddr().String()
	pc.Close()
	if res := Check(context.Background(), check(dead, "A")); res.Kind != KindError {
		t.Fatalf("res = %+v", res)
	}
}

func TestSystemServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")
	old := resolvConf
	t.Cleanup(func() { resolvConf = old })
	resolvConf = path

	os.WriteFile(path, []byte("# comment\nsearch lan\nnameserver 192.0.2.53\nnameserver 2001:db8::53\nnameserver bogus\nnameserver 192.0.2.53\noptions ndots:2\n"), 0o600)
	if got := systemServers(); !slices.Equal(got, []string{"192.0.2.53:53", "[2001:db8::53]:53"}) {
		t.Errorf("servers = %q", got)
	}
	os.WriteFile(path, []byte("search lan\n"), 0o600)
	if got := systemServers(); !slices.Equal(got, []string{"127.0.0.1:53"}) {
		t.Errorf("fallback = %q", got)
	}
	resolvConf = filepath.Join(t.TempDir(), "missing")
	if got := systemServers(); !slices.Equal(got, []string{"127.0.0.1:53"}) {
		t.Errorf("missing file = %q", got)
	}
}

func TestResolverAddr(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.1":          "192.0.2.1:53",
		"192.0.2.1:5353":     "192.0.2.1:5353",
		"2001:db8::1":        "[2001:db8::1]:53",
		"[2001:db8::1]:5353": "[2001:db8::1]:5353",
	} {
		if got, err := ResolverAddr(in); err != nil || got != want {
			t.Errorf("ResolverAddr(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"dns.example", "192.0.2.1:0", "192.0.2.1:99999", "fe80::1%eth0", "udp://192.0.2.1", ""} {
		if _, err := ResolverAddr(bad); err == nil {
			t.Errorf("ResolverAddr(%q) accepted", bad)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Config{Hostname: "example.com", QueryType: "A", Timeout: time.Second}
	with := func(f func(*Config)) Config { c := ok; f(&c); return c }
	cases := []struct {
		cfg   Config
		valid bool
	}{
		{ok, true},
		{with(func(c *Config) {
			c.Hostname = "_dmarc.example.com"
			c.QueryType = "TXT"
			c.Expected = []string{" v=DMARC1 "}
		}), true},
		{with(func(c *Config) { c.Resolver = "[2001:db8::1]:53"; c.MatchMode = MatchAny }), true},
		{with(func(c *Config) { c.QueryType = "MX"; c.Expected = []string{"mx.example.com", "10 mx.example.com"} }), true},
		{with(func(c *Config) { c.Hostname = "" }), false},
		{with(func(c *Config) { c.Hostname = "192.0.2.1" }), false},
		{with(func(c *Config) { c.QueryType = "SOA" }), false},
		{with(func(c *Config) { c.QueryType = "a" }), false},
		{with(func(c *Config) { c.Resolver = "dns.google" }), false},
		{with(func(c *Config) { c.MatchMode = "some" }), false},
		{with(func(c *Config) { c.Timeout = 0 }), false},
		{with(func(c *Config) { c.Expected = []string{"2001:db8::1"} }), false},
		{with(func(c *Config) { c.QueryType = "AAAA"; c.Expected = []string{"192.0.2.1"} }), false},
		{with(func(c *Config) { c.QueryType = "CNAME"; c.Expected = []string{"not a name"} }), false},
		{with(func(c *Config) { c.QueryType = "MX"; c.Expected = []string{"70000 mx.example.com"} }), false},
		{with(func(c *Config) { c.QueryType = "MX"; c.Expected = []string{"1 2 3"} }), false},
		{with(func(c *Config) { c.QueryType = "TXT"; c.Expected = []string{""} }), false},
		{with(func(c *Config) { c.QueryType = "TXT"; c.Expected = []string{strings.Repeat("x", maxTXT+1)} }), false},
		{with(func(c *Config) { c.Expected = ips(MaxExpected + 1) }), false},
		{with(func(c *Config) { c.Expected = append(ips(MaxExpected), "192.0.2.1") }), true},
	}
	for i, c := range cases {
		if err := c.cfg.Validate(); (err == nil) != c.valid {
			t.Errorf("case %d (%+v): %v", i, c.cfg, err)
		}
	}
}

// ips returns n distinct IPv4 addresses.
func ips(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("192.0.2.%d", i+1)
	}
	return out
}

func TestNormalizeExpected(t *testing.T) {
	cases := []struct {
		qtype string
		in    []string
		want  []string
	}{
		{"A", []string{"192.0.2.1", " 192.0.2.1 ", "192.0.2.2"}, []string{"192.0.2.1", "192.0.2.2"}},
		{"AAAA", []string{"2001:DB8::1", "2001:db8:0::1"}, []string{"2001:db8::1"}},
		{"CNAME", []string{"Target.Example.com.", "target.example.com"}, []string{"target.example.com"}},
		{"MX", []string{"MX.example.com", "10 mx.example.com", "mx.example.com."}, []string{"mx.example.com", "10 mx.example.com"}},
		{"TXT", []string{"v=spf1", "V=SPF1", "v=spf1"}, []string{"v=spf1", "V=SPF1"}},
		{"NS", nil, []string{}},
	}
	for _, c := range cases {
		got, err := NormalizeExpected(c.qtype, c.in)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s %q: got %q, %v; want %q", c.qtype, c.in, got, err, c.want)
		}
	}
	if _, err := NormalizeExpected("SOA", nil); err == nil {
		t.Error("SOA accepted")
	}
	if _, err := NormalizeExpected("A", ips(MaxExpected+1)); err == nil {
		t.Error("21 distinct values accepted")
	}
}
