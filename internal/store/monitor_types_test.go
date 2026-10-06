package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func tcpSample(name string) MonitorInput {
	return MonitorInput{Type: TypeTCP, Name: name, Enabled: true, TCP: TCPConfig{Host: "db.internal", Port: 5432}}
}

func dnsSample(name string) MonitorInput {
	return MonitorInput{Type: TypeDNS, Name: name, Enabled: true,
		DNS: DNSConfig{Hostname: "example.com", QueryType: "MX", Expected: []string{"10 MX.example.com.", "10 mx.example.com"}}}
}

func heartbeatSample(t *testing.T, name string) MonitorInput {
	t.Helper()
	_, hash, err := NewHeartbeatToken()
	if err != nil {
		t.Fatal(err)
	}
	return MonitorInput{Type: TypeHeartbeat, Name: name, Enabled: true,
		Heartbeat: HeartbeatSettings{ExpectedIntervalSeconds: 3600, GraceSeconds: 300, SourceLabel: " nightly backup ", TokenHash: hash}}
}

func TestCreateEachType(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)

	tcp := create(t, d, tcpSample("tcp"))
	if c, err := GetTCPConfig(ctx, d.Reader, tcp); err != nil || c != (TCPConfig{"db.internal", 5432}) {
		t.Errorf("tcp config = %+v, %v", c, err)
	}

	icmp := create(t, d, MonitorInput{Type: TypeICMP, Name: "icmp", Enabled: true, ICMP: ICMPConfig{Host: " 2001:db8::1 "}})
	if c, err := GetICMPConfig(ctx, d.Reader, icmp); err != nil || c.Host != "2001:db8::1" {
		t.Errorf("icmp config = %+v, %v", c, err)
	}

	dns := create(t, d, dnsSample("dns"))
	c, err := GetDNSConfig(ctx, d.Reader, dns)
	want := DNSConfig{Hostname: "example.com", QueryType: "MX", Expected: []string{"10 mx.example.com"}, MatchMode: "all"}
	if err != nil || !reflect.DeepEqual(c, want) {
		t.Errorf("dns config = %+v, %v; want %+v", c, err, want)
	}
	plain := create(t, d, MonitorInput{Type: TypeDNS, Name: "dns2", DNS: DNSConfig{Hostname: "example.org"}})
	if c, err := GetDNSConfig(ctx, d.Reader, plain); err != nil || c.QueryType != "A" || c.Expected != nil || c.Resolver != "" {
		t.Errorf("dns defaults = %+v, %v", c, err)
	}

	hb := create(t, d, heartbeatSample(t, "hb"))
	h, err := GetHeartbeatConfig(ctx, d.Reader, hb)
	if err != nil || h.ExpectedInterval.Seconds() != 3600 || h.Grace.Seconds() != 300 || h.SourceLabel != "nightly backup" {
		t.Errorf("heartbeat config = %+v, %v", h, err)
	}
	m, _ := GetMonitor(ctx, d.Reader, hb)
	if m.Type != TypeHeartbeat || m.IntervalSeconds != 3600 {
		t.Errorf("heartbeat row type %s interval %d", m.Type, m.IntervalSeconds)
	}

	got, err := Targets(ctx, d.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wantTargets := map[string]string{tcp: "db.internal:5432", icmp: "2001:db8::1", dns: "example.com MX", plain: "example.org A", hb: "nightly backup"}
	if !reflect.DeepEqual(got, wantTargets) {
		t.Errorf("Targets = %v", got)
	}
	for id, want := range wantTargets {
		if one, err := Target(ctx, d.Reader, id); err != nil || one != want {
			t.Errorf("Target(%s) = %q, %v; want %q", id, one, err, want)
		}
	}
	if _, err := Target(ctx, d.Reader, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Target(missing) = %v", err)
	}
}

func TestTargetsBracketsIPv6Port(t *testing.T) {
	d := testDB(t)
	in := tcpSample("v6")
	in.TCP.Host = "2001:db8::5"
	id := create(t, d, in)
	got, _ := Targets(context.Background(), d.Reader)
	if got[id] != "[2001:db8::5]:5432" {
		t.Errorf("target = %q", got[id])
	}
}

func TestHeartbeatNeedsTokenHash(t *testing.T) {
	in := heartbeatSample(t, "hb")
	in.Heartbeat.TokenHash = nil
	if _, err := CreateMonitor(context.Background(), testDB(t), in, now); err == nil {
		t.Error("created without a token hash")
	}
}

func TestUpdateKeepsTypeAndToken(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	hb := create(t, d, heartbeatSample(t, "hb"))
	token, err := SetHeartbeatToken(ctx, d, hb)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := HashHeartbeatToken(token)
	if _, err := RecordBeat(ctx, d, hash, now); err != nil {
		t.Fatal(err)
	}

	up := heartbeatSample(t, "hb renamed")
	up.Heartbeat.ExpectedIntervalSeconds, up.Heartbeat.GraceSeconds, up.Heartbeat.SourceLabel = 60, 0, ""
	if err := UpdateMonitor(ctx, d, hb, up, now); err != nil {
		t.Fatal(err)
	}
	h, _ := GetHeartbeatConfig(ctx, d.Reader, hb)
	if h.ExpectedInterval.Seconds() != 60 || h.Grace != 0 || h.SourceLabel != "" || h.LastBeatAt == nil {
		t.Errorf("after update = %+v", h)
	}
	if id, err := RecordBeat(ctx, d, hash, now); err != nil || id != hb {
		t.Errorf("token after update: %q, %v", id, err)
	}
	if m, _ := GetMonitor(ctx, d.Reader, hb); m.IntervalSeconds != 60 {
		t.Errorf("interval = %d", m.IntervalSeconds)
	}

	// Another type's input never converts a monitor.
	if err := UpdateMonitor(ctx, d, hb, tcpSample("x"), now); !errors.Is(err, ErrNotFound) {
		t.Errorf("type change: %v", err)
	}
	tcp := create(t, d, tcpSample("tcp"))
	in := tcpSample("tcp")
	in.TCP.Port = 6543
	if err := UpdateMonitor(ctx, d, tcp, in, now); err != nil {
		t.Fatal(err)
	}
	if c, _ := GetTCPConfig(ctx, d.Reader, tcp); c.Port != 6543 {
		t.Errorf("port = %d", c.Port)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM tcp_monitor_config`); n != 1 {
		t.Errorf("%d tcp config rows", n)
	}
}

func TestValidateTypeRules(t *testing.T) {
	many := make([]string, 0, 21)
	for i := range 21 {
		many = append(many, fmt.Sprintf("192.0.2.%d", i+1))
	}
	cases := map[string]struct {
		in    MonitorInput
		field string
	}{
		"unknown type":   {MonitorInput{Type: "smtp", Name: "x"}, "type"},
		"tcp host":       {MonitorInput{Type: TypeTCP, Name: "x", TCP: TCPConfig{Host: "tcp://db", Port: 1}}, "host"},
		"tcp host port":  {MonitorInput{Type: TypeTCP, Name: "x", TCP: TCPConfig{Host: "db:5432", Port: 1}}, "host"},
		"tcp port zero":  {MonitorInput{Type: TypeTCP, Name: "x", TCP: TCPConfig{Host: "db"}}, "port"},
		"tcp port big":   {MonitorInput{Type: TypeTCP, Name: "x", TCP: TCPConfig{Host: "db", Port: 65536}}, "port"},
		"icmp host":      {MonitorInput{Type: TypeICMP, Name: "x", ICMP: ICMPConfig{Host: "https://x"}}, "host"},
		"icmp empty":     {MonitorInput{Type: TypeICMP, Name: "x"}, "host"},
		"dns hostname":   {MonitorInput{Type: TypeDNS, Name: "x", DNS: DNSConfig{Hostname: "a b"}}, "hostname"},
		"dns ip":         {MonitorInput{Type: TypeDNS, Name: "x", DNS: DNSConfig{Hostname: "192.0.2.1"}}, "hostname"},
		"dns type":       {MonitorInput{Type: TypeDNS, Name: "x", DNS: DNSConfig{Hostname: "e.com", QueryType: "SOA"}}, "query_type"},
		"dns resolver":   {MonitorInput{Type: TypeDNS, Name: "x", DNS: DNSConfig{Hostname: "e.com", Resolver: "dns.google"}}, "resolver"},
		"dns match":      {MonitorInput{Type: TypeDNS, Name: "x", DNS: DNSConfig{Hostname: "e.com", MatchMode: "some"}}, "match_mode"},
		"dns expected":   {MonitorInput{Type: TypeDNS, Name: "x", DNS: DNSConfig{Hostname: "e.com", Expected: []string{"2001:db8::1"}}}, "expected"},
		"dns 21 values":  {MonitorInput{Type: TypeDNS, Name: "x", DNS: DNSConfig{Hostname: "e.com", Expected: many}}, "expected"},
		"hb interval":    {MonitorInput{Type: TypeHeartbeat, Name: "x", Heartbeat: HeartbeatSettings{ExpectedIntervalSeconds: 5}}, "expected_interval_seconds"},
		"hb interval 0":  {MonitorInput{Type: TypeHeartbeat, Name: "x"}, "expected_interval_seconds"},
		"hb grace":       {MonitorInput{Type: TypeHeartbeat, Name: "x", Heartbeat: HeartbeatSettings{ExpectedIntervalSeconds: 60, GraceSeconds: -1}}, "grace_seconds"},
		"hb grace large": {MonitorInput{Type: TypeHeartbeat, Name: "x", Heartbeat: HeartbeatSettings{ExpectedIntervalSeconds: 60, GraceSeconds: 86401}}, "grace_seconds"},
	}
	long := MonitorInput{Type: TypeHeartbeat, Name: "x", Heartbeat: HeartbeatSettings{ExpectedIntervalSeconds: 60}}
	for range 101 {
		long.Heartbeat.SourceLabel += "é"
	}
	cases["hb label"] = struct {
		in    MonitorInput
		field string
	}{long, "source_label"}
	for name, c := range cases {
		in := c.in
		in.applyDefaults()
		errs := in.validate("")
		if errs[c.field] == "" || len(errs) != 1 {
			t.Errorf("%s: errors = %v, want only %s", name, errs, c.field)
		}
	}

	// A heartbeat's own interval may be long without a timeout rule.
	ok := MonitorInput{Type: TypeHeartbeat, Name: "x", TimeoutMS: 99999999, Heartbeat: HeartbeatSettings{ExpectedIntervalSeconds: 86400}}
	ok.applyDefaults()
	if errs := ok.validate(""); len(errs) != 0 {
		t.Errorf("heartbeat errors = %v", errs)
	}
	// 20 distinct values plus duplicates are fine.
	dup := MonitorInput{Type: TypeDNS, Name: "x", DNS: DNSConfig{Hostname: "e.com", Expected: append(many[:20:20], many[0], " "+many[1])}}
	dup.applyDefaults()
	if errs := dup.validate(""); len(errs) != 0 || len(dup.DNS.Expected) != 20 {
		t.Errorf("dedup errors = %v, %d values", errs, len(dup.DNS.Expected))
	}
}
