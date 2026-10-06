package integration

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/drilonrecica/sinjal/internal/monitor/icmpcheck"
	"github.com/drilonrecica/sinjal/internal/store"
)

// M4-07: the monitor types of M4 end to end in the real binary: each type
// up and down against a local target, ICMP either working or failing with
// its explicit permission error, and scenario 20 (heartbeat expiry) driven
// through the form, the push endpoint and the regenerate route.

// localDNS answers A queries for any name with 192.0.2.1 on a local UDP
// port and returns its address.
func localDNS(t *testing.T) string {
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
			if out, err := resp.Pack(); err == nil {
				pc.WriteTo(out, from)
			}
		}
	}()
	return pc.LocalAddr().String()
}

// openTCP accepts and closes connections on a local port; closedTCP is a
// local port nothing listens on.
func openTCP(t *testing.T) int {
	t.Helper()
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
	return ln.Addr().(*net.TCPAddr).Port
}

func closedTCP(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestMonitorTypeScenarios(t *testing.T) {
	skipShort(t)
	resolver := localDNS(t)
	dir := t.TempDir()
	if err := start(t, dir).stop(); err != nil {
		t.Fatal(err)
	}
	d := openDB(t, dir)
	create := func(in store.MonitorInput) string {
		t.Helper()
		in.Enabled, in.RetryDelayMS = true, 300
		id, err := store.CreateMonitor(context.Background(), d, in, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	dns := func(name string, expected ...string) store.MonitorInput {
		return store.MonitorInput{Type: store.TypeDNS, Name: name,
			DNS: store.DNSConfig{Hostname: "service.example", QueryType: "A", Resolver: resolver, Expected: expected}}
	}
	tcpUp := create(store.MonitorInput{Type: store.TypeTCP, Name: "tcp up", TCP: store.TCPConfig{Host: "127.0.0.1", Port: openTCP(t)}})
	tcpDown := create(store.MonitorInput{Type: store.TypeTCP, Name: "tcp down", TCP: store.TCPConfig{Host: "127.0.0.1", Port: closedTCP(t)}})
	dnsUp := create(dns("dns up", "192.0.2.1"))
	dnsDown := create(dns("dns down", "192.0.2.9"))
	ping := create(store.MonitorInput{Type: store.TypeICMP, Name: "ping", ICMP: store.ICMPConfig{Host: "127.0.0.1"}})
	// A one-second interval (validation allows nothing below ten).
	if _, err := d.Writer.Exec(`UPDATE monitors SET interval_seconds = 1, timeout_ms = 900`); err != nil {
		t.Fatal(err)
	}

	s := start(t, dir)
	state := func(id, want string) func() bool {
		return func() bool { return monitorRow(t, d, id).State == want }
	}
	waitFor(t, s, "the open TCP port up", state(tcpUp, "up"))
	waitFor(t, s, "the closed TCP port down", state(tcpDown, "down"))
	waitFor(t, s, "the matching DNS answer up", state(dnsUp, "up"))
	waitFor(t, s, "the mismatching DNS answer down", state(dnsDown, "down"))
	waitFor(t, s, "a ping result", func() bool { return len(checkResults(t, d, ping)) > 0 })
	if err := s.stop(); err != nil {
		t.Fatalf("exit after SIGTERM: %v\n%s", err, s.logs)
	}

	for _, r := range checkResults(t, d, tcpDown) {
		if r.success || r.kind != "connect" {
			t.Errorf("closed port result: %+v", r)
		}
	}
	rs := checkResults(t, d, dnsDown)
	if last := rs[len(rs)-1]; last.kind != "dns_mismatch" || !strings.Contains(last.snippet, "missing: 192.0.2.9") ||
		!strings.Contains(last.snippet, "answer: 192.0.2.1") {
		t.Errorf("mismatch result: %+v", last)
	}

	// ICMP works here, or every check says plainly that it may not ping:
	// never a DOWN for some other reason.
	pings := checkResults(t, d, ping)
	switch m := monitorRow(t, d, ping); {
	case m.State == "up" && pings[0].success:
		t.Log("ICMP permitted: loopback is up")
	default:
		for _, r := range pings {
			if r.success || r.kind != icmpcheck.KindPermission || r.message != icmpcheck.PermissionHint {
				t.Errorf("ping result neither success nor the permission failure: %+v", r)
			}
		}
		t.Log("ICMP not permitted: the permission failure is surfaced")
	}
}

// TestICMPPermissionSurfaced runs the binary where it may not ping: in a
// new, unprivileged user and network namespace (`unshare -Un`) the process
// has no CAP_NET_RAW and the namespace's ping_group_range is empty, as in
// a container runtime that allows neither. Every check must then fail with
// the permission kind and the hint naming both fixes, and the monitor goes
// DOWN for that reason, not for an unrelated one. The server's HTTP port
// is unreachable from outside the namespace, so the test reads the
// database and stops it with SIGTERM.
func TestICMPPermissionSurfaced(t *testing.T) {
	skipShort(t)
	if err := exec.Command("unshare", "-Un", "true").Run(); err != nil {
		t.Skipf("unprivileged user namespaces unavailable (%v); the permission path is covered by icmpcheck tests", err)
	}
	dir := t.TempDir()
	if err := start(t, dir).stop(); err != nil {
		t.Fatal(err)
	}
	d := openDB(t, dir)
	id, err := store.CreateMonitor(context.Background(), d, store.MonitorInput{Type: store.TypeICMP, Name: "ping",
		Enabled: true, RetryDelayMS: 300, ICMP: store.ICMPConfig{Host: "127.0.0.1"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer.Exec(`UPDATE monitors SET interval_seconds = 1, timeout_ms = 900`); err != nil {
		t.Fatal(err)
	}

	logs := &lockedBuffer{}
	cmd := exec.Command("unshare", "-Un", binary(t), "serve")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SINJAL_DATA_DIR=" + dir,
		"SINJAL_LISTEN=0.0.0.0:8080", "SINJAL_LOG_FORMAT=json"}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			<-done
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for monitorRow(t, d, id).State != "down" {
		select {
		case err := <-done:
			t.Fatalf("server exited: %v\n%s", err, logs)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("not DOWN within 15 s\n%s", logs)
		}
		time.Sleep(25 * time.Millisecond)
	}
	cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit after SIGTERM: %v\n%s", err, logs)
		}
	case <-time.After(stopTimeout):
		t.Fatalf("no exit after SIGTERM\n%s", logs)
	}

	rs := checkResults(t, d, id)
	if len(rs) < 2 {
		t.Fatalf("%d results", len(rs))
	}
	for _, r := range rs {
		if r.success || r.kind != icmpcheck.KindPermission || r.message != icmpcheck.PermissionHint {
			t.Errorf("result is not the permission failure: %+v", r)
		}
	}
	if !strings.Contains(icmpcheck.PermissionHint, "ping_group_range") || !strings.Contains(icmpcheck.PermissionHint, "CAP_NET_RAW") {
		t.Errorf("hint names no fix: %q", icmpcheck.PermissionHint)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM incidents WHERE monitor_id = ? AND ended_at IS NULL`, id); n != 1 {
		t.Errorf("%d open incidents", n)
	}
}

var pushToken = regexp.MustCompile(`/api/v1/heartbeat/([A-Za-z0-9_-]{43})`)

// Scenario 20, heartbeat expiry: a heartbeat monitor created through the
// form reveals its push URL once; beats by path and by bearer token keep
// it UP; silence past the expected interval plus grace makes it DOWN with
// heartbeat_missed (after the usual confirmation retry); a beat recovers
// it; a regenerated token replaces the old one at once. No token reaches
// the log.
func TestHeartbeatExpiryScenario(t *testing.T) {
	skipShort(t)
	dir := t.TempDir()
	s := start(t, dir)
	createAdmin(t, s)
	c := signIn(t, s, "admin", adminPassword)
	d := openDB(t, dir)

	rs, page := c.post("/monitors", url.Values{"type": {"heartbeat"}, "name": {"nightly backup"},
		"expected_interval": {"60"}, "grace": {"0"}, "source_label": {"db-1"}, "retry_delay": {"0.3"}, "enabled": {"1"}})
	m := pushToken.FindStringSubmatch(page)
	if rs.StatusCode != http.StatusOK || m == nil || rs.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create = %d, push URL found %v:\n%s", rs.StatusCode, m != nil, page)
	}
	token := m[1]
	id := queryString(t, d, `SELECT id FROM monitors WHERE name = 'nightly backup'`)
	// Shorten the period to 2 s + 1 s grace (validation allows nothing
	// below ten); the next beat reschedules on it.
	if _, err := d.Writer.Exec(`UPDATE heartbeat_monitor_config SET expected_interval_seconds = 2, grace_seconds = 1 WHERE monitor_id = ?`, id); err != nil {
		t.Fatal(err)
	}

	beat := func(tok string, bearer bool) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, s.base+"/api/v1/heartbeat/"+tok, nil)
		if bearer {
			req, _ = http.NewRequest(http.MethodPost, s.base+"/api/v1/heartbeat", nil)
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	state := func(want string) func() bool {
		return func() bool { return monitorRow(t, d, id).State == want }
	}

	if code := beat(token, false); code != http.StatusNoContent {
		t.Fatalf("beat by path = %d", code)
	}
	waitFor(t, s, "UP after the first beat", state("up"))
	time.Sleep(time.Second)
	if code := beat(token, true); code != http.StatusNoContent {
		t.Fatalf("beat by bearer = %d", code)
	}
	if st := monitorRow(t, d, id).State; st != "up" {
		t.Fatalf("state after a timely beat = %s", st)
	}

	// Silence: late after 3 s, DOWN after the confirmation retry.
	silent := time.Now()
	waitFor(t, s, "DOWN after silence", state("down"))
	if waited := time.Since(silent); waited < 3*time.Second {
		t.Errorf("DOWN after %v, before the period of interval plus grace", waited)
	}
	res := checkResults(t, d, id)
	last := res[len(res)-1]
	if last.success || last.kind != "heartbeat_missed" || !strings.Contains(last.message, "no heartbeat since") ||
		!strings.Contains(last.message, "expected every 2s, grace 1s") {
		t.Errorf("missed result: %+v", last)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM incidents WHERE monitor_id = ? AND ended_at IS NULL`, id); n != 1 {
		t.Errorf("%d open incidents", n)
	}

	if code := beat(token, false); code != http.StatusNoContent {
		t.Fatalf("recovering beat = %d", code)
	}
	waitFor(t, s, "UP after a beat", state("up"))
	if n := count(t, d, `SELECT COUNT(*) FROM incidents WHERE monitor_id = ? AND ended_at IS NOT NULL`, id); n != 1 {
		t.Errorf("%d ended incidents", n)
	}

	// The detail page shows the heartbeat facts, never the token.
	if rs, page := c.get("/monitors/" + id); rs.StatusCode != 200 || !strings.Contains(page, "Expects a beat every") || strings.Contains(page, token) {
		t.Errorf("detail = %d", rs.StatusCode)
	}

	// Regenerate (the session is a fresh login, so recent enough).
	rs, page = c.post("/monitors/"+id+"/heartbeat/token", nil)
	m = pushToken.FindStringSubmatch(page)
	if rs.StatusCode != http.StatusOK || m == nil || m[1] == token {
		t.Fatalf("regenerate = %d", rs.StatusCode)
	}
	if code := beat(token, false); code != http.StatusNotFound {
		t.Errorf("old token after regenerate = %d", code)
	}
	if code := beat(m[1], true); code != http.StatusNoContent {
		t.Errorf("new token = %d", code)
	}
	notLogged(t, s, token, m[1])
	if err := s.stop(); err != nil {
		t.Fatalf("exit after SIGTERM: %v\n%s", err, s.logs)
	}
}
