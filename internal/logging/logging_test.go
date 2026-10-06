package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/drilonrecica/sinjal/internal/secret"
)

func TestTextHasSubsystem(t *testing.T) {
	var buf bytes.Buffer
	Sub(New(&buf, "text", "info"), "scheduler").Info("tick", "monitor_id", 7)
	out := buf.String()
	for _, want := range []string{"time=", "level=INFO", "subsystem=scheduler", "msg=tick", "monitor_id=7"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output %q is missing %q", out, want)
		}
	}
}

func TestJSONHasSubsystem(t *testing.T) {
	var buf bytes.Buffer
	Sub(New(&buf, "json", "info"), "http").Warn("slow", "ms", 900)
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not valid JSON: %v: %s", err, buf.String())
	}
	if m["subsystem"] != "http" || m["level"] != "WARN" || m["msg"] != "slow" || m["time"] == nil {
		t.Errorf("unexpected JSON record: %v", m)
	}
}

func TestLevelFiltering(t *testing.T) {
	tests := []struct {
		level     string
		wantDebug bool
		wantWarn  bool
	}{
		{"debug", true, true},
		{"info", false, true},
		{"warn", false, true},
		{"error", false, false},
		{"bogus", false, true}, // falls back to info
	}
	for _, tt := range tests {
		var buf bytes.Buffer
		l := New(&buf, "text", tt.level)
		l.Debug("d")
		l.Warn("w")
		out := buf.String()
		if got := strings.Contains(out, "msg=d"); got != tt.wantDebug {
			t.Errorf("level %s: debug logged = %v, want %v", tt.level, got, tt.wantDebug)
		}
		if got := strings.Contains(out, "msg=w"); got != tt.wantWarn {
			t.Errorf("level %s: warn logged = %v, want %v", tt.level, got, tt.wantWarn)
		}
	}
}

func TestSecretsNeverLogged(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		var buf bytes.Buffer
		New(&buf, format, "info").Info("config", "smtp_password", secret.String("s3cr3t"))
		if strings.Contains(buf.String(), "s3cr3t") {
			t.Errorf("%s output leaked a secret: %s", format, buf.String())
		}
	}
}
