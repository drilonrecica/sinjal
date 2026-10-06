package engine

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/monitor"
	"github.com/drilonrecica/sinjal/internal/monitor/httpcheck"
	"github.com/drilonrecica/sinjal/internal/results"
	"github.com/drilonrecica/sinjal/internal/secret"
	"github.com/drilonrecica/sinjal/internal/store"
)

func TestBuildHTTPConfig(t *testing.T) {
	m := store.Monitor{ID: "m1", TimeoutMS: 2500}
	c := store.HTTPConfig{
		URL: "https://example.com/health", Method: "POST", FollowRedirects: true, ExpectedStatus: "200-299,404",
		BodyContains: "ok", BodyNotContains: "error",
		JSONAssertions:  `[{"path":"$.status","op":"equals","value":"up"}]`,
		Headers:         `[{"name":"Accept","value":"application/json"}]`,
		RequestBody:     `{"ping":true}`,
		CustomUserAgent: "probe/1", MaxBodyBytes: 4096, InsecureSkipVerify: true,
		ProxyURL: "http://proxy.internal:3128", IPFamily: "ipv4",
	}
	secrets := map[string]secret.String{"auth.bearer": "tok"}

	got, err := buildHTTPConfig(m, c, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if got.Expected.String() != "200-299,404" {
		t.Fatalf("Expected = %q", got.Expected.String())
	}
	want := httpcheck.Config{
		URL: c.URL, Method: "POST", Body: c.RequestBody, UserAgent: "probe/1", Expected: got.Expected,
		Headers:      []monitor.Header{{Name: "Accept", Value: "application/json"}},
		BodyContains: "ok", BodyNotContains: "error",
		JSONAssertions: []monitor.JSONAssertion{{Path: "$.status", Op: "equals", Value: []byte(`"up"`)}},
		MaxBodyBytes:   4096, FollowRedirects: true, Insecure: true,
		ProxyURL: c.ProxyURL, IPFamily: "ipv4", Timeout: 2500 * time.Millisecond, Secrets: secrets,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config:\n got %+v\nwant %+v", got, want)
	}
}

func TestBuildHTTPConfigRejectsUnusableText(t *testing.T) {
	ok := store.HTTPConfig{URL: "https://example.com", Method: "GET", ExpectedStatus: "200"}
	for name, edit := range map[string]func(*store.HTTPConfig){
		"status":     func(c *store.HTTPConfig) { c.ExpectedStatus = "2xx" },
		"headers":    func(c *store.HTTPConfig) { c.Headers = "{" },
		"assertions": func(c *store.HTTPConfig) { c.JSONAssertions = `[{"path":1}]` },
	} {
		c := ok
		edit(&c)
		if _, err := buildHTTPConfig(store.Monitor{TimeoutMS: 1000}, c, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := buildHTTPConfig(store.Monitor{TimeoutMS: 1000}, ok, nil); err != nil {
		t.Fatal(err)
	}
}

func TestToResult(t *testing.T) {
	started := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	expiry := started.Add(40 * 24 * time.Hour)
	tls := &httpcheck.TLSInfo{NotAfter: expiry, Issuer: "Test CA", DaysRemaining: 40}

	up := toResult("m1", true, httpcheck.Result{Started: started, Duration: 120 * time.Millisecond, Success: true, StatusCode: 204, TLS: tls})
	want := results.Result{MonitorID: "m1", CheckedAt: started, Duration: 120 * time.Millisecond, Success: true, Status: "204",
		Metadata: up.Metadata, TLSNotAfter: &expiry}
	if !reflect.DeepEqual(up, want) || !strings.Contains(up.Metadata, `"issuer":"Test CA"`) {
		t.Fatalf("success: %+v", up)
	}

	// Expiry checks off: the certificate stays in the metadata but does
	// not drive the warning.
	if r := toResult("m1", false, httpcheck.Result{Started: started, Success: true, StatusCode: 200, TLS: tls}); r.TLSNotAfter != nil || r.Metadata == "" {
		t.Fatalf("expiry checks off: %+v", r)
	}

	// No response at all: no protocol status.
	down := toResult("m1", true, httpcheck.Result{Started: started, Kind: httpcheck.KindConnect, Message: "connection refused", Snippet: "x"})
	if down.Success || down.Status != "" || down.Kind != "connect" || down.Message != "connection refused" || down.Snippet != "x" || down.Metadata != "" || down.TLSNotAfter != nil {
		t.Fatalf("failure: %+v", down)
	}
}
