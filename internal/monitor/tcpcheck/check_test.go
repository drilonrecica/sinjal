package tcpcheck

import (
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// listen starts a listener on addr that reports every accepted connection's
// first read result (nil error is never expected: the check sends nothing,
// so a clean close shows up as io.EOF).
func listen(t *testing.T, network, addr string) (net.Listener, <-chan error) {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Skipf("cannot listen on %s: %v", addr, err)
	}
	t.Cleanup(func() { ln.Close() })
	reads := make(chan error, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetReadDeadline(time.Now().Add(2 * time.Second))
				_, err := c.Read(make([]byte, 1))
				reads <- err
			}()
		}
	}()
	return ln, reads
}

func cfgFor(t *testing.T, ln net.Listener) Config {
	t.Helper()
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(port)
	return Config{Host: host, Port: p, Timeout: 2 * time.Second}
}

func TestCheckSuccessClosesPromptly(t *testing.T) {
	ln, reads := listen(t, "tcp", "127.0.0.1:0")
	res := Check(context.Background(), cfgFor(t, ln))
	if !res.Success || res.Kind != "" || res.Message != "" {
		t.Fatalf("res = %+v", res)
	}
	if res.Duration <= 0 || res.Duration > time.Second || res.Finished.Before(res.Started) {
		t.Errorf("duration = %s", res.Duration)
	}
	select {
	case err := <-reads:
		if err != io.EOF {
			t.Errorf("server saw %v, want a clean close (EOF)", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the connection was not closed promptly")
	}
}

func TestCheckIPv6(t *testing.T) {
	ln, _ := listen(t, "tcp", "[::1]:0")
	if res := Check(context.Background(), cfgFor(t, ln)); !res.Success {
		t.Errorf("res = %+v", res)
	}
}

func TestCheckFailureKinds(t *testing.T) {
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	closedAddr := closed.Addr().String()
	closed.Close()
	_, port, _ := net.SplitHostPort(closedAddr)
	p, _ := strconv.Atoi(port)

	tests := map[string]struct {
		cfg  Config
		kind string
	}{
		"refused":      {Config{Host: "127.0.0.1", Port: p, Timeout: 2 * time.Second}, KindConnect},
		"no such host": {Config{Host: "sinjal-no-such-host.invalid", Port: 80, Timeout: 5 * time.Second}, KindDNS},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			res := Check(context.Background(), tc.cfg)
			if res.Success || res.Kind != tc.kind || res.Message == "" {
				t.Errorf("res = %+v, want kind %s", res, tc.kind)
			}
		})
	}
}

func TestCheckTimeout(t *testing.T) {
	// A zero-length deadline fails the dial before it can connect, for any
	// target, without depending on the network.
	ln, _ := listen(t, "tcp", "127.0.0.1:0")
	cfg := cfgFor(t, ln)
	cfg.Timeout = time.Nanosecond
	res := Check(context.Background(), cfg)
	if res.Success || res.Kind != KindTimeout || !strings.Contains(res.Message, "1ns") {
		t.Errorf("res = %+v", res)
	}
}

func TestCheckCancelledIsUnknown(t *testing.T) {
	ln, _ := listen(t, "tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := Check(ctx, cfgFor(t, ln))
	if res.Success || res.Kind != KindUnknown {
		t.Errorf("res = %+v", res)
	}
}

func TestCheckWithoutTimeoutUsesCallerContext(t *testing.T) {
	ln, _ := listen(t, "tcp", "127.0.0.1:0")
	cfg := cfgFor(t, ln)
	cfg.Timeout = 0
	if res := Check(context.Background(), cfg); !res.Success {
		t.Errorf("res = %+v", res)
	}
}

func TestValidate(t *testing.T) {
	ok := Config{Host: "example.com", Port: 443, Timeout: time.Second}
	tests := map[string]struct {
		mut  func(*Config)
		fail bool
	}{
		"valid name":     {func(c *Config) {}, false},
		"valid ipv4":     {func(c *Config) { c.Host = "10.0.0.1" }, false},
		"valid ipv6":     {func(c *Config) { c.Host = "::1" }, false},
		"trailing dot":   {func(c *Config) { c.Host = "example.com." }, false},
		"underscore":     {func(c *Config) { c.Host = "_sip._tcp.example.com" }, false},
		"port 1":         {func(c *Config) { c.Port = 1 }, false},
		"port 65535":     {func(c *Config) { c.Port = 65535 }, false},
		"port 0":         {func(c *Config) { c.Port = 0 }, true},
		"port 65536":     {func(c *Config) { c.Port = 65536 }, true},
		"port negative":  {func(c *Config) { c.Port = -1 }, true},
		"empty host":     {func(c *Config) { c.Host = "" }, true},
		"scheme":         {func(c *Config) { c.Host = "http://example.com" }, true},
		"host with port": {func(c *Config) { c.Host = "example.com:80" }, true},
		"bracketed ipv6": {func(c *Config) { c.Host = "[::1]" }, true},
		"zone":           {func(c *Config) { c.Host = "fe80::1%eth0" }, true},
		"space":          {func(c *Config) { c.Host = "exa mple.com" }, true},
		"empty label":    {func(c *Config) { c.Host = "a..com" }, true},
		"leading hyphen": {func(c *Config) { c.Host = "-a.com" }, true},
		"long label":     {func(c *Config) { c.Host = strings.Repeat("a", 64) + ".com" }, true},
		"long name":      {func(c *Config) { c.Host = strings.Repeat("a.", 127) + "a" }, true},
		"only a dot":     {func(c *Config) { c.Host = "." }, true},
		"zero timeout":   {func(c *Config) { c.Timeout = 0 }, true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			c := ok
			tc.mut(&c)
			if err := c.Validate(); (err != nil) != tc.fail {
				t.Errorf("Validate(%+v) = %v, fail=%v", c, err, tc.fail)
			}
		})
	}
}
