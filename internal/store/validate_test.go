package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestValidateRules(t *testing.T) {
	for name, c := range map[string]struct {
		mod   func(*HTTPMonitor)
		field string
	}{
		"interval too short":   {func(m *HTTPMonitor) { m.IntervalSeconds = 9 }, "interval_seconds"},
		"interval too long":    {func(m *HTTPMonitor) { m.IntervalSeconds = 86401 }, "interval_seconds"},
		"negative timeout":     {func(m *HTTPMonitor) { m.TimeoutMS = -1 }, "timeout_ms"},
		"timeout = interval":   {func(m *HTTPMonitor) { m.IntervalSeconds, m.TimeoutMS = 10, 10000 }, "timeout_ms"},
		"failure threshold 0":  {func(m *HTTPMonitor) { m.FailureThreshold = -1 }, "failure_threshold"},
		"failure threshold up": {func(m *HTTPMonitor) { m.FailureThreshold = 101 }, "failure_threshold"},
		"success threshold":    {func(m *HTTPMonitor) { m.SuccessThreshold = -3 }, "success_threshold"},
		"retry delay":          {func(m *HTTPMonitor) { m.RetryDelayMS = -1 }, "retry_delay_ms"},
		"ftp url":              {func(m *HTTPMonitor) { m.Config.URL = "ftp://example.com" }, "url"},
		"relative url":         {func(m *HTTPMonitor) { m.Config.URL = "/health" }, "url"},
		"no host":              {func(m *HTTPMonitor) { m.Config.URL = "http://" }, "url"},
		"userinfo in url":      {func(m *HTTPMonitor) { m.Config.URL = "https://u:p@example.com" }, "url"},
		"long url":             {func(m *HTTPMonitor) { m.Config.URL = "https://e.com/" + strings.Repeat("a", 2048) }, "url"},
		"method":               {func(m *HTTPMonitor) { m.Config.Method = "PUT" }, "method"},
		"body with GET":        {func(m *HTTPMonitor) { m.Config.RequestBody = "x" }, "request_body"},
		"big body":             {func(m *HTTPMonitor) { m.Config.Method, m.Config.RequestBody = "POST", strings.Repeat("x", 64<<10+1) }, "request_body"},
		"status":               {func(m *HTTPMonitor) { m.Config.ExpectedStatus = "2xx" }, "expected_status"},
		"body cap low":         {func(m *HTTPMonitor) { m.Config.MaxBodyBytes = 1023 }, "max_body_bytes"},
		"body cap high":        {func(m *HTTPMonitor) { m.Config.MaxBodyBytes = 1048577 }, "max_body_bytes"},
		"contains":             {func(m *HTTPMonitor) { m.Config.BodyContains = strings.Repeat("x", 4097) }, "body_contains"},
		"not contains":         {func(m *HTTPMonitor) { m.Config.BodyNotContains = strings.Repeat("x", 4097) }, "body_not_contains"},
		"json malformed":       {func(m *HTTPMonitor) { m.Config.JSONAssertions = `{"path":"$"}` }, "json_assertions"},
		"json path":            {func(m *HTTPMonitor) { m.Config.JSONAssertions = `[{"path":"$..a","op":"exists"}]` }, "json_assertions"},
		"json value missing":   {func(m *HTTPMonitor) { m.Config.JSONAssertions = `[{"path":"$.a","op":"equals"}]` }, "json_assertions"},
		"json too many": {func(m *HTTPMonitor) {
			m.Config.JSONAssertions = "[" + strings.Repeat(`{"path":"$","op":"exists"},`, 20) + `{"path":"$","op":"exists"}]`
		}, "json_assertions"},
		"headers object":       {func(m *HTTPMonitor) { m.Config.Headers = `{"X":"y"}` }, "headers"},
		"header name":          {func(m *HTTPMonitor) { m.Config.Headers = `[{"name":"X Y","value":"1"}]` }, "headers"},
		"header crlf":          {func(m *HTTPMonitor) { m.Config.Headers = `[{"name":"X","value":"a\r\nB: c"}]` }, "headers"},
		"header authorization": {func(m *HTTPMonitor) { m.Config.Headers = `[{"name":"authorization","value":"Bearer x"}]` }, "headers"},
		"header host":          {func(m *HTTPMonitor) { m.Config.Headers = `[{"name":"Host","value":"x"}]` }, "headers"},
		"header duplicate":     {func(m *HTTPMonitor) { m.Config.Headers = `[{"name":"X","value":"1"},{"name":"x","value":"2"}]` }, "headers"},
		"user agent":           {func(m *HTTPMonitor) { m.Config.CustomUserAgent = "a\nb" }, "custom_user_agent"},
		"tls days zero":        {func(m *HTTPMonitor) { m.Config.TLSWarningDays = "[30,0]" }, "tls_warning_days"},
		"tls days text":        {func(m *HTTPMonitor) { m.Config.TLSWarningDays = `["30"]` }, "tls_warning_days"},
		"tls days many":        {func(m *HTTPMonitor) { m.Config.TLSWarningDays = "[1,2,3,4,5,6,7,8,9,10,11]" }, "tls_warning_days"},
		"tls days year":        {func(m *HTTPMonitor) { m.Config.TLSWarningDays = "[366]" }, "tls_warning_days"},
		"proxy scheme":         {func(m *HTTPMonitor) { m.Config.ProxyURL = "ftp://proxy:21" }, "proxy_url"},
		"proxy credentials":    {func(m *HTTPMonitor) { m.Config.ProxyURL = "http://u:p@proxy:3128" }, "proxy_url"},
		"ip family":            {func(m *HTTPMonitor) { m.Config.IPFamily = "ipv5" }, "ip_family"},
		"tag":                  {func(m *HTTPMonitor) { m.Tags = []string{"a\x00"} }, "tags"},
	} {
		in := sample("x")
		c.mod(&in)
		in.applyDefaults()
		errs := in.validate("")
		if errs[c.field] == "" {
			t.Errorf("%s: errors = %v, want a %s error", name, errs, c.field)
		}
		if len(errs) != 1 {
			t.Errorf("%s: errors = %v, want only %s", name, errs, c.field)
		}
	}
}

func TestValidateAcceptsAndNormalises(t *testing.T) {
	in := sample("x")
	in.IntervalSeconds, in.TimeoutMS = 10, 9999
	c := &in.Config
	c.Method = "post"
	c.RequestBody = `{"ping":1}`
	c.ExpectedStatus = "204, 200-299"
	c.MaxBodyBytes = 1024
	c.TLSWarningDays = "[7,30,14,7]"
	c.Headers = `[{"name":"X-Trace","value":"a\tb"},{"name":"Accept","value":"application/json"}]`
	c.JSONAssertions = `[{"path":"$.status","op":"equals","value":"up"},{"path":"$[\"x-v\"]","op":"exists"}]`
	c.ProxyURL = "socks5://proxy:1080"
	c.IPFamily = "ipv6"
	c.CustomUserAgent = "probe/1"
	in.applyDefaults()
	if errs := in.validate(""); len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
	if c.Method != "POST" || c.ExpectedStatus != "200-299" || c.TLSWarningDays != "[30,14,7]" {
		t.Errorf("not normalised: method=%s status=%s days=%s", c.Method, c.ExpectedStatus, c.TLSWarningDays)
	}
}

func TestValidateCollectsEveryField(t *testing.T) {
	in := sample("x")
	in.Name, in.IntervalSeconds, in.Config.URL, in.Config.Method = "", 1, "nope", "DELETE"
	in.applyDefaults()
	errs := in.validate("")
	for _, f := range []string{"name", "interval_seconds", "url", "method"} {
		if errs[f] == "" {
			t.Errorf("missing %s in %v", f, errs)
		}
	}
}

func TestCreateChecksReferences(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	in := sample("x")
	in.ParentMonitorID, in.NotificationProfileID, in.Name = "missing", "nope", ""
	_, err := CreateHTTPMonitor(ctx, d, in, now)
	var fe FieldErrors
	if !errors.As(err, &fe) || fe["parent_monitor_id"] == "" || fe["notification_profile_id"] == "" || fe["name"] == "" {
		t.Fatalf("error = %v, want parent, profile and name errors together", err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM monitors`); n != 0 {
		t.Errorf("a rejected create wrote %d monitors", n)
	}

	if _, err := d.Writer.Exec(`INSERT INTO notification_profiles (id, name, created_at, updated_at) VALUES ('p1', 'Default', ?, ?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	root, err := CreateHTTPMonitor(ctx, d, sample("root"), now)
	if err != nil {
		t.Fatal(err)
	}
	in = sample("child")
	in.ParentMonitorID, in.NotificationProfileID = root, "p1"
	if _, err := CreateHTTPMonitor(ctx, d, in, now); err != nil {
		t.Fatalf("existing parent and profile rejected: %v", err)
	}
}

func TestUpdateRejectsDependencyCycles(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	a, _ := CreateHTTPMonitor(ctx, d, sample("a"), now)
	in := sample("b")
	in.ParentMonitorID = a
	b, _ := CreateHTTPMonitor(ctx, d, in, now)
	in = sample("c")
	in.ParentMonitorID = b
	c, _ := CreateHTTPMonitor(ctx, d, in, now)

	var fe FieldErrors
	for name, parent := range map[string]string{"self": a, "two levels": c, "one level": b} {
		up := sample("a")
		up.ParentMonitorID = parent
		if err := UpdateHTTPMonitor(ctx, d, a, up, now); !errors.As(err, &fe) || fe["parent_monitor_id"] == "" {
			t.Errorf("%s: a -> %s must be a cycle, got %v", name, parent, err)
		}
	}
	m, _ := GetMonitor(ctx, d.Reader, a)
	if m.ParentMonitorID != "" {
		t.Errorf("a rejected update changed the parent to %q", m.ParentMonitorID)
	}
	// Moving c under a is fine: a is a root.
	up := sample("c")
	up.ParentMonitorID = a
	if err := UpdateHTTPMonitor(ctx, d, c, up, now); err != nil {
		t.Errorf("re-parenting without a cycle: %v", err)
	}
}

func TestSecretNamesAndAuthExclusivity(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	id, _ := CreateHTTPMonitor(ctx, d, sample("a"), now)
	var ie *InputError
	for _, n := range []string{"auth", "token", "header.", "header.X Y", "auth.digest"} {
		if err := SetSecret(ctx, d, k, id, n, []byte("v"), now); !errors.As(err, &ie) {
			t.Errorf("secret name %q: %v, want an InputError", n, err)
		}
	}
	if err := SetSecret(ctx, d, k, id, "header.X-Api-Key", []byte("k"), now); err != nil {
		t.Fatal(err)
	}
	if err := SetSecret(ctx, d, k, id, "auth.basic", []byte("u:p"), now); err != nil {
		t.Fatal(err)
	}
	if err := SetSecret(ctx, d, k, id, "auth.basic", []byte("u:p2"), now); err != nil {
		t.Errorf("replacing basic auth: %v", err)
	}
	if err := SetSecret(ctx, d, k, id, "auth.bearer", []byte("t"), now); !errors.As(err, &ie) {
		t.Errorf("bearer next to basic: %v, want an InputError", err)
	}
	_ = DeleteSecret(ctx, d, id, "auth.basic")
	if err := SetSecret(ctx, d, k, id, "auth.bearer", []byte("t"), now); err != nil {
		t.Errorf("bearer after removing basic: %v", err)
	}
}
