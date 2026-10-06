package config

import (
	"net/netip"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func load(t *testing.T, env map[string]string) (Config, error) {
	t.Helper()
	getenv := func(k string) string { return env[k] }
	environ := func() []string {
		var out []string
		for k, v := range env {
			out = append(out, k+"="+v)
		}
		return out
	}
	return Load(getenv, environ)
}

func TestDefaults(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/data" || c.Listen != ":8080" || c.BaseURL != "" {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if c.LogFormat != "text" || c.LogLevel != "info" || c.Timezone.String() != "UTC" {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if len(c.TrustedProxies) != 0 {
		t.Errorf("no proxies must be trusted by default, got %v", c.TrustedProxies)
	}
	want := min(32, max(8, runtime.NumCPU()*4))
	if c.Workers != want {
		t.Errorf("Workers = %d, want %d", c.Workers, want)
	}
}

func TestOverrides(t *testing.T) {
	c, err := load(t, map[string]string{
		"SINJAL_DATA_DIR":        "relative/data",
		"SINJAL_LISTEN":          "127.0.0.1:9000",
		"SINJAL_BASE_URL":        "https://sinjal.example.com/",
		"SINJAL_TRUSTED_PROXIES": "172.16.0.0/12, 10.0.0.1, ::1",
		"SINJAL_LOG_FORMAT":      "JSON",
		"SINJAL_LOG_LEVEL":       "Debug",
		"SINJAL_TIMEZONE":        "Europe/Belgrade",
		"SINJAL_WORKERS":         "12",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(c.DataDir) || !strings.HasSuffix(c.DataDir, filepath.Join("relative", "data")) {
		t.Errorf("DataDir = %q", c.DataDir)
	}
	if c.Listen != "127.0.0.1:9000" || c.BaseURL != "https://sinjal.example.com" {
		t.Errorf("Listen/BaseURL = %q / %q", c.Listen, c.BaseURL)
	}
	wantProxies := []netip.Prefix{
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("10.0.0.1/32"),
		netip.MustParsePrefix("::1/128"),
	}
	if len(c.TrustedProxies) != 3 {
		t.Fatalf("TrustedProxies = %v", c.TrustedProxies)
	}
	for i, p := range wantProxies {
		if c.TrustedProxies[i] != p {
			t.Errorf("proxy %d = %v, want %v", i, c.TrustedProxies[i], p)
		}
	}
	if c.LogFormat != "json" || c.LogLevel != "debug" {
		t.Errorf("log = %q/%q", c.LogFormat, c.LogLevel)
	}
	if c.Timezone.String() != "Europe/Belgrade" || c.Workers != 12 {
		t.Errorf("tz/workers = %v/%d", c.Timezone, c.Workers)
	}
}

func TestInvalid(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"listen no port", map[string]string{"SINJAL_LISTEN": "localhost"}, "SINJAL_LISTEN"},
		{"listen port zero", map[string]string{"SINJAL_LISTEN": ":0"}, "SINJAL_LISTEN"},
		{"listen port too big", map[string]string{"SINJAL_LISTEN": ":70000"}, "SINJAL_LISTEN"},
		{"listen named port", map[string]string{"SINJAL_LISTEN": ":http"}, "SINJAL_LISTEN"},
		{"base url relative", map[string]string{"SINJAL_BASE_URL": "sinjal.example.com"}, "SINJAL_BASE_URL"},
		{"base url scheme", map[string]string{"SINJAL_BASE_URL": "ftp://x.example"}, "SINJAL_BASE_URL"},
		{"base url query", map[string]string{"SINJAL_BASE_URL": "https://x.example/?a=1"}, "SINJAL_BASE_URL"},
		{"base url creds", map[string]string{"SINJAL_BASE_URL": "https://u:p@x.example"}, "SINJAL_BASE_URL"},
		{"proxy garbage", map[string]string{"SINJAL_TRUSTED_PROXIES": "10.0.0.0/8,nope"}, `"nope"`},
		{"log format", map[string]string{"SINJAL_LOG_FORMAT": "xml"}, "SINJAL_LOG_FORMAT"},
		{"log level", map[string]string{"SINJAL_LOG_LEVEL": "trace"}, "SINJAL_LOG_LEVEL"},
		{"timezone", map[string]string{"SINJAL_TIMEZONE": "Mars/Base"}, "SINJAL_TIMEZONE"},
		{"workers zero", map[string]string{"SINJAL_WORKERS": "0"}, "SINJAL_WORKERS"},
		{"workers text", map[string]string{"SINJAL_WORKERS": "many"}, "SINJAL_WORKERS"},
		{"workers huge", map[string]string{"SINJAL_WORKERS": "1000"}, "SINJAL_WORKERS"},
		{"unknown var", map[string]string{"SINJAL_LOG_LEVl": "debug"}, "SINJAL_LOG_LEVl: unknown variable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.env)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestAllErrorsReported(t *testing.T) {
	_, err := load(t, map[string]string{
		"SINJAL_LISTEN":    "bad",
		"SINJAL_LOG_LEVEL": "loud",
		"SINJAL_WORKERS":   "-1",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, name := range []string{"SINJAL_LISTEN", "SINJAL_LOG_LEVEL", "SINJAL_WORKERS"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q is missing %s", err, name)
		}
	}
}

func TestBlankValuesUseDefaults(t *testing.T) {
	c, err := load(t, map[string]string{"SINJAL_LISTEN": "  ", "SINJAL_LOG_LEVEL": ""})
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.LogLevel != "info" {
		t.Errorf("got %q / %q", c.Listen, c.LogLevel)
	}
}

func TestNonSinjalVariablesIgnored(t *testing.T) {
	if _, err := load(t, map[string]string{"HOME": "/root", "SINJALX": "1"}); err != nil {
		t.Fatal(err)
	}
}
