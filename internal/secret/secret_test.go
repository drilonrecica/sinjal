package secret

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const value = "hunter2-very-secret"

func TestFormattingRedacts(t *testing.T) {
	s := String(value)
	for _, verb := range []string{"%s", "%v", "%+v", "%#v", "%q", "%x"} {
		got := fmt.Sprintf(verb, s)
		if strings.Contains(got, value) {
			t.Errorf("%s leaked the value: %q", verb, got)
		}
	}
	if got := fmt.Sprint(s); got != "[REDACTED]" {
		t.Errorf("Sprint = %q", got)
	}
}

func TestStructFormattingRedacts(t *testing.T) {
	type cfg struct {
		User string
		Pass String
	}
	got := fmt.Sprintf("%v %+v %#v", cfg{"a", value}, cfg{"a", value}, cfg{"a", value})
	if strings.Contains(got, value) {
		t.Errorf("struct formatting leaked the value: %q", got)
	}
}

func TestJSONRedacts(t *testing.T) {
	type payload struct {
		Token String            `json:"token"`
		Map   map[string]String `json:"map"`
	}
	b, err := json.Marshal(payload{Token: value, Map: map[string]String{"k": value}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), value) {
		t.Errorf("JSON leaked the value: %s", b)
	}
	if want := `{"token":"[REDACTED]","map":{"k":"[REDACTED]"}}`; string(b) != want {
		t.Errorf("JSON = %s, want %s", b, want)
	}
}

func TestSlogRedacts(t *testing.T) {
	for name, mk := range map[string]func(*bytes.Buffer) slog.Handler{
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			l := slog.New(mk(&buf))
			l.Info("login", "password", String(value), slog.Group("g", "inner", String(value)))
			out := buf.String()
			if strings.Contains(out, value) {
				t.Errorf("slog leaked the value: %s", out)
			}
			if !strings.Contains(out, "[REDACTED]") {
				t.Errorf("slog output has no marker: %s", out)
			}
		})
	}
}

func TestReveal(t *testing.T) {
	if got := String(value).Reveal(); got != value {
		t.Errorf("Reveal = %q", got)
	}
}
