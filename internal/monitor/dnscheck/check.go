package dnscheck

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Failure kinds (docs/30), stored in check_results.error_kind.
const (
	KindTimeout  = "timeout"
	KindNXDomain = "dns_nxdomain"  // the name does not exist
	KindNoAnswer = "dns_no_answer" // no record of the query type
	KindMismatch = "dns_mismatch"  // expected values not satisfied
	KindError    = "dns_error"     // resolver unreachable, error rcode, bad reply
	KindUnknown  = "unknown"       // cancelled by the caller
)

const (
	udpSize    = 1232 // EDNS0 payload size advertised (DNS flag day 2020)
	snippetCap = 1024
)

// resolvConf is read for the system's nameservers. A variable for tests.
var resolvConf = "/etc/resolv.conf"

// Result is the outcome of one check. Answers are the normalized records
// of the query type (A/AAAA canonical, names lowercase without the
// trailing dot, MX "preference host", TXT strings joined).
type Result struct {
	Started  time.Time
	Finished time.Time
	Duration time.Duration
	Success  bool
	Kind     string // "" on success
	Message  string
	Snippet  string // dns_mismatch: missing values and the answers
	Answers  []string
	Server   string // the resolver that answered
}

// Check sends the query and evaluates the answer. It never returns an
// error: every failure is a Result with a kind. cfg must be valid.
func Check(ctx context.Context, cfg Config) Result {
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	res := Result{Started: time.Now()}
	fail := func(kind, msg string) Result {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			kind, msg = KindUnknown, "check cancelled"
		case errors.Is(ctx.Err(), context.DeadlineExceeded) && kind == KindError:
			kind, msg = KindTimeout, fmt.Sprintf("no answer within %s", cfg.Timeout)
		}
		return finish(res, kind, msg)
	}

	qtype := queryTypes[cfg.QueryType]
	name, err := dnsmessage.NewName(strings.TrimSuffix(cfg.Hostname, ".") + ".")
	if err != nil {
		return fail(KindError, err.Error())
	}
	q := dnsmessage.Question{Name: name, Type: qtype, Class: dnsmessage.ClassINET}

	servers := systemServers()
	if cfg.Resolver != "" {
		addr, err := ResolverAddr(cfg.Resolver)
		if err != nil {
			return fail(KindError, err.Error())
		}
		servers = []string{addr}
	}
	resp, server, err := query(ctx, servers, q)
	res.Server = server
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		// The socket's deadline can pass a moment before ctx reports it.
		return fail(KindTimeout, fmt.Sprintf("no answer within %s", cfg.Timeout))
	case err != nil:
		return fail(KindError, err.Error())
	}
	switch resp.RCode {
	case dnsmessage.RCodeSuccess:
	case dnsmessage.RCodeNameError:
		return fail(KindNXDomain, fmt.Sprintf("%s does not exist (NXDOMAIN)", cfg.Hostname))
	default:
		return fail(KindError, fmt.Sprintf("resolver answered %s", rcodeName(resp.RCode)))
	}
	res.Answers = answers(resp, qtype)
	if len(res.Answers) == 0 {
		return fail(KindNoAnswer, fmt.Sprintf("no %s record for %s", cfg.QueryType, cfg.Hostname))
	}
	if missing := unmatched(cfg, res.Answers); missing != nil {
		res.Snippet = capped(fmt.Sprintf("missing: %s\nanswer: %s", strings.Join(missing, ", "), strings.Join(res.Answers, ", ")))
		return fail(KindMismatch, fmt.Sprintf("the %s answer does not match the expected values", cfg.QueryType))
	}
	res = finish(res, "", "")
	res.Success = true
	return res
}

func finish(r Result, kind, msg string) Result {
	r.Finished = time.Now()
	r.Duration = r.Finished.Sub(r.Started)
	r.Kind, r.Message = kind, msg
	return r
}

// unmatched returns the expected values the answer does not satisfy, or
// nil when it satisfies the match mode (always, without expected values).
func unmatched(cfg Config, answers []string) []string {
	var missing []string
	for _, v := range cfg.Expected {
		e, err := parseExpected(cfg.QueryType, v)
		if err != nil || !e.in(answers) {
			missing = append(missing, v)
		}
	}
	if cfg.MatchMode == MatchAny && len(missing) < len(cfg.Expected) {
		return nil
	}
	return missing
}

// in reports whether an answer satisfies e.
func (e expected) in(answers []string) bool {
	for _, a := range answers {
		if e.host != "" {
			if _, host, ok := strings.Cut(a, " "); ok && host == e.host {
				return true
			}
		} else if a == e.value {
			return true
		}
	}
	return false
}

// answers collects the records of the query type from the answer section,
// whatever their owner name: an A query for an alias returns the CNAME
// chain and the target's addresses.
func answers(m *dnsmessage.Message, qtype dnsmessage.Type) []string {
	var out []string
	for _, rr := range m.Answers {
		if rr.Header.Type != qtype || rr.Header.Class != dnsmessage.ClassINET {
			continue
		}
		var v string
		switch b := rr.Body.(type) {
		case *dnsmessage.AResource:
			v = netip.AddrFrom4(b.A).String()
		case *dnsmessage.AAAAResource:
			v = netip.AddrFrom16(b.AAAA).String()
		case *dnsmessage.CNAMEResource:
			v = normName(b.CNAME.String())
		case *dnsmessage.NSResource:
			v = normName(b.NS.String())
		case *dnsmessage.MXResource:
			v = mxValue(b.Pref, normName(b.MX.String()))
		case *dnsmessage.TXTResource:
			v = strings.Join(b.TXT, "")
		default:
			continue
		}
		out = append(out, v)
	}
	return out
}

// query asks each server in turn until one answers; a server that cannot
// be reached gets an equal share of the time left. A reply with an error
// rcode is an answer.
func query(ctx context.Context, servers []string, q dnsmessage.Question) (*dnsmessage.Message, string, error) {
	var lastErr error
	for i, server := range servers {
		sctx := ctx
		if dl, ok := ctx.Deadline(); ok {
			var cancel context.CancelFunc
			share := time.Until(dl) / time.Duration(len(servers)-i)
			sctx, cancel = context.WithTimeout(ctx, share)
			defer cancel()
		}
		m, err := exchange(sctx, server, q)
		if err == nil {
			return m, server, nil
		}
		lastErr = fmt.Errorf("%s: %w", server, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, "", lastErr
}

// exchange sends one query over UDP and repeats it over TCP when the
// answer was truncated.
func exchange(ctx context.Context, server string, q dnsmessage.Question) (*dnsmessage.Message, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	id := binary.BigEndian.Uint16(b[:])
	req, err := buildQuery(id, q)
	if err != nil {
		return nil, err
	}
	m, err := exchangeUDP(ctx, server, req, id, q)
	if err != nil || !m.Truncated {
		return m, err
	}
	return exchangeTCP(ctx, server, req, id, q)
}

func buildQuery(id uint16, q dnsmessage.Question) ([]byte, error) {
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{ID: id, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(q); err != nil {
		return nil, err
	}
	if err := b.StartAdditionals(); err != nil {
		return nil, err
	}
	var opt dnsmessage.ResourceHeader
	if err := opt.SetEDNS0(udpSize, dnsmessage.RCodeSuccess, false); err != nil {
		return nil, err
	}
	if err := b.OPTResource(opt, dnsmessage.OPTResource{}); err != nil {
		return nil, err
	}
	return b.Finish()
}

func dial(ctx context.Context, network, server string) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, network, server)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	// A cancelled check (shutdown) must not wait for its deadline.
	stop := context.AfterFunc(ctx, func() { c.SetDeadline(time.Now()) })
	return &stopConn{Conn: c, stop: stop}, nil
}

type stopConn struct {
	net.Conn
	stop func() bool
}

func (c *stopConn) Close() error {
	c.stop()
	return c.Conn.Close()
}

// exchangeUDP sends the query and waits for the reply with its id and
// question; anything else arriving on the socket is ignored.
func exchangeUDP(ctx context.Context, server string, req []byte, id uint16, q dnsmessage.Question) (*dnsmessage.Message, error) {
	c, err := dial(ctx, "udp", server)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if _, err := c.Write(req); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return nil, err
		}
		if m, ok := reply(buf[:n], id, q); ok {
			return m, nil
		}
	}
}

// exchangeTCP sends the query with its two-byte length prefix and reads
// one reply.
func exchangeTCP(ctx context.Context, server string, req []byte, id uint16, q dnsmessage.Question) (*dnsmessage.Message, error) {
	c, err := dial(ctx, "tcp", server)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	out := binary.BigEndian.AppendUint16(make([]byte, 0, len(req)+2), uint16(len(req)))
	if _, err := c.Write(append(out, req...)); err != nil {
		return nil, err
	}
	var size [2]byte
	if _, err := io.ReadFull(c, size[:]); err != nil {
		return nil, err
	}
	buf := make([]byte, binary.BigEndian.Uint16(size[:]))
	if _, err := io.ReadFull(c, buf); err != nil {
		return nil, err
	}
	m, ok := reply(buf, id, q)
	if !ok {
		return nil, errors.New("reply does not match the query")
	}
	return m, nil
}

// reply parses b and reports whether it answers the query id for q.
func reply(b []byte, id uint16, q dnsmessage.Question) (*dnsmessage.Message, bool) {
	var m dnsmessage.Message
	if err := m.Unpack(b); err != nil || !m.Response || m.ID != id || len(m.Questions) != 1 {
		return nil, false
	}
	got := m.Questions[0]
	if got.Type != q.Type || got.Class != q.Class || !strings.EqualFold(got.Name.String(), q.Name.String()) {
		return nil, false
	}
	return &m, true
}

// systemServers are the nameservers of resolv.conf, as host:port, or the
// local resolver when it names none (the stdlib's default too).
func systemServers() []string {
	var out []string
	f, err := os.Open(resolvConf)
	if err == nil {
		defer f.Close()
		s := bufio.NewScanner(f)
		for s.Scan() {
			fields := strings.Fields(s.Text())
			if len(fields) < 2 || fields[0] != "nameserver" {
				continue
			}
			a, err := netip.ParseAddr(fields[1])
			if err != nil {
				continue
			}
			if addr := net.JoinHostPort(a.String(), "53"); !slices.Contains(out, addr) {
				out = append(out, addr)
			}
		}
	}
	if len(out) == 0 {
		return []string{"127.0.0.1:53"}
	}
	return out
}

func rcodeName(r dnsmessage.RCode) string {
	switch r {
	case dnsmessage.RCodeServerFailure:
		return "SERVFAIL"
	case dnsmessage.RCodeRefused:
		return "REFUSED"
	case dnsmessage.RCodeFormatError:
		return "FORMERR"
	case dnsmessage.RCodeNotImplemented:
		return "NOTIMP"
	}
	return fmt.Sprintf("rcode %d", r)
}

// capped makes s valid UTF-8 (TXT records may hold any bytes) and cuts it
// to snippetCap bytes; a rune split by the cut is dropped.
func capped(s string) string {
	if len(s) > snippetCap {
		s = s[:snippetCap-len("…")] + "…"
	}
	return strings.ToValidUTF8(s, "")
}
