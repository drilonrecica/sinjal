package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"version", []string{"version"}, 0, "sinjal dev\n", ""},
		{"default is serve", nil, 1, "", "sinjal: serve is not implemented yet"},
		{"explicit serve", []string{"serve"}, 1, "", "sinjal: serve is not implemented yet"},
		{"unknown command", []string{"bogus"}, 2, "", `unknown command "bogus"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestVersionOverride(t *testing.T) {
	old := version
	version = "1.2.3"
	defer func() { version = old }()

	var stdout bytes.Buffer
	run([]string{"version"}, &stdout, &bytes.Buffer{})
	if got := stdout.String(); got != "sinjal 1.2.3\n" {
		t.Errorf("stdout = %q", got)
	}
}

func TestServeRejectsInvalidConfig(t *testing.T) {
	t.Setenv("SINJAL_LOG_LEVEL", "loud")
	t.Setenv("SINJAL_WORKERS", "0")

	var stderr bytes.Buffer
	if code := run([]string{"serve"}, &bytes.Buffer{}, &stderr); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{"invalid configuration", "SINJAL_LOG_LEVEL", "SINJAL_WORKERS"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr %q is missing %q", stderr.String(), want)
		}
	}
}
