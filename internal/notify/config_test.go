package notify

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/drilonrecica/sinjal/internal/secret"
)

func values(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func validSMTP() SMTP {
	return SMTP{Host: "smtp.example.com", Port: 587, Security: SecuritySTARTTLS, Username: "u", Password: "pw",
		From: "sinjal@example.com", To: []string{"ops@example.com"}}
}

func TestValidConfigsHaveNoErrors(t *testing.T) {
	for _, c := range []Config{
		validSMTP(),
		SMTP{Host: "10.0.0.5", Port: 25, Security: SecurityTLS, From: "a@b.co", To: []string{"x@y.io", "z@y.io"}}, // no auth
		Telegram{BotToken: "123456:ABC-def_1", ChatID: "-1001234"},
		Telegram{BotToken: "1:a", ChatID: "@mychannel"},
		Discord{WebhookURL: "https://discord.com/api/webhooks/1/abc"},
		Webhook{URL: "http://hooks.local:8080/in"},
		Webhook{URL: "https://hooks.example.com/x", HeaderName: "Authorization", HeaderValue: "Bearer t"},
	} {
		if e := c.Validate(); e != nil {
			t.Errorf("%s %v: %v", c.Type(), c, e)
		}
	}
}

func TestValidationFindsEveryBadField(t *testing.T) {
	cases := []struct {
		name string
		c    Config
		want []string
	}{
		{"smtp empty", SMTP{}, []string{"host", "port", "security", "from", "to"}},
		{"smtp host with scheme", func() Config { c := validSMTP(); c.Host = "smtp://x"; return c }(), []string{"host"}},
		{"smtp host with port", func() Config { c := validSMTP(); c.Host = "x.com:25"; return c }(), []string{"host"}},
		{"smtp port high", func() Config { c := validSMTP(); c.Port = 65536; return c }(), []string{"port"}},
		{"smtp user without password", func() Config { c := validSMTP(); c.Password = ""; return c }(), []string{"password"}},
		{"smtp password without user", func() Config { c := validSMTP(); c.Username = ""; return c }(), []string{"password"}},
		{"smtp display name", func() Config { c := validSMTP(); c.From = "Sinjal <a@b.co>"; return c }(), []string{"from"}},
		{"smtp header injection", func() Config { c := validSMTP(); c.To = []string{"a@b.co\r\nBcc: x@y.z"}; return c }(), []string{"to"}},
		{"smtp bad recipient", func() Config { c := validSMTP(); c.To = []string{"a@b.co", "nope"}; return c }(), []string{"to"}},
		{"smtp too many recipients", func() Config { c := validSMTP(); c.To = make([]string, MaxRecipients+1); return c }(), []string{"to"}},
		{"telegram empty", Telegram{}, []string{"bot_token", "chat_id"}},
		{"telegram token shape", Telegram{BotToken: "abc", ChatID: "1"}, []string{"bot_token"}},
		{"telegram chat", Telegram{BotToken: "1:a", ChatID: "my chat"}, []string{"chat_id"}},
		{"discord empty", Discord{}, []string{"webhook_url"}},
		{"discord http", Discord{WebhookURL: "http://discord.com/x"}, []string{"webhook_url"}},
		{"discord no host", Discord{WebhookURL: "https:///x"}, []string{"webhook_url"}},
		{"webhook empty", Webhook{}, []string{"url"}},
		{"webhook scheme", Webhook{URL: "ftp://x.com"}, []string{"url"}},
		{"webhook userinfo", Webhook{URL: "https://u:p@x.com"}, []string{"url"}},
		{"webhook header name only", Webhook{URL: "https://x.com", HeaderName: "X-A"}, []string{"header_value"}},
		{"webhook header value only", Webhook{URL: "https://x.com", HeaderValue: "v"}, []string{"header_value"}},
		{"webhook bad header name", Webhook{URL: "https://x.com", HeaderName: "X A", HeaderValue: "v"}, []string{"header_name"}},
		{"webhook newline in value", Webhook{URL: "https://x.com", HeaderName: "X-A", HeaderValue: "a\nb"}, []string{"header_value"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.c.Validate()
			if len(got) != len(tc.want) {
				t.Fatalf("errors = %v, want fields %v", got, tc.want)
			}
			for _, f := range tc.want {
				if got[f] == "" {
					t.Errorf("no message for %s: %v", f, got)
				}
			}
		})
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	for _, c := range []Config{
		validSMTP(),
		Telegram{BotToken: "1:abc", ChatID: "-5"},
		Discord{WebhookURL: "https://discord.com/api/webhooks/1/abc"},
		Webhook{URL: "https://x.com/in", HeaderName: "Authorization", HeaderValue: "Bearer t"},
	} {
		b, err := Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Unmarshal(c.Type(), b)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", c) {
			t.Errorf("round trip: got %#v, want %#v", got, c)
		}
		if got.Type() != c.Type() {
			t.Errorf("type = %s, want %s", got.Type(), c.Type())
		}
	}
	if _, err := Unmarshal("sms", []byte(`{}`)); err == nil {
		t.Error("an unknown type must be refused")
	}
	if _, err := Unmarshal(TypeSMTP, []byte(`{`)); err == nil {
		t.Error("broken JSON must be refused")
	}
}

func TestMarshalCarriesRealSecrets(t *testing.T) {
	// The plaintext is what gets encrypted; a redacted marshal would store
	// "[REDACTED]" as the password.
	b, _ := Marshal(validSMTP())
	if !strings.Contains(string(b), `"password":"pw"`) || strings.Contains(string(b), "REDACTED") {
		t.Errorf("stored form = %s", b)
	}
}

func TestSecretsNeverPrint(t *testing.T) {
	cs := []Config{
		SMTP{Host: "h", Port: 25, Security: SecurityTLS, Username: "u", Password: "TOPSECRET", From: "a@b.co", To: []string{"c@d.io"}},
		Telegram{BotToken: "123456:TOPSECRET", ChatID: "1"},
		Discord{WebhookURL: "https://discord.com/api/webhooks/1/TOPSECRET"},
		Webhook{URL: "https://x.com", HeaderName: "A", HeaderValue: "TOPSECRET"},
	}
	for _, c := range cs {
		out := fmt.Sprintf("%v %+v %#v %s", c, c, c, slog.Any("c", c))
		if strings.Contains(out, "TOPSECRET") {
			t.Errorf("%s config leaks a secret: %s", c.Type(), out)
		}
		for k, v := range c.Fields() {
			if strings.Contains(v, "TOPSECRET") || k == "password" || k == "bot_token" {
				t.Errorf("%s Fields exposes %s=%q", c.Type(), k, v)
			}
		}
	}
}

func TestSecretsSetAndFields(t *testing.T) {
	if got := validSMTP().SecretsSet(); !got["password"] {
		t.Errorf("SMTP SecretsSet = %v", got)
	}
	if got := (SMTP{}).SecretsSet(); got["password"] {
		t.Errorf("empty SMTP SecretsSet = %v", got)
	}
	f := validSMTP().Fields()
	if f["host"] != "smtp.example.com" || f["port"] != "587" || f["to"] != "ops@example.com" || f["username"] != "u" {
		t.Errorf("SMTP Fields = %v", f)
	}
	if (SMTP{}).Fields()["port"] != "" {
		t.Error("an unset port must show empty, not 0")
	}
}

func TestWithSecretsFromKeepsBlankSecrets(t *testing.T) {
	old := validSMTP()
	edit := old
	edit.Password = ""
	if got := edit.WithSecretsFrom(old).(SMTP); got.Password != "pw" {
		t.Error("a blank password must keep the stored one")
	}
	edit.Password = "new"
	if got := edit.WithSecretsFrom(old).(SMTP); got.Password != "new" {
		t.Error("a given password must replace the stored one")
	}
	// A password belongs to its user: a new user name needs a new password.
	edit = old
	edit.Username, edit.Password = "other", ""
	if got := edit.WithSecretsFrom(old).(SMTP); got.Password != "" {
		t.Error("a changed user name must not inherit the old password")
	}

	if got := (Telegram{ChatID: "1"}).WithSecretsFrom(Telegram{BotToken: "1:a"}).(Telegram); got.BotToken != "1:a" {
		t.Error("telegram token not kept")
	}
	if got := (Discord{}).WithSecretsFrom(Discord{WebhookURL: "https://d/x"}).(Discord); got.WebhookURL != "https://d/x" {
		t.Error("discord URL not kept")
	}
	w := Webhook{URL: "https://x", HeaderName: "A"}.WithSecretsFrom(Webhook{HeaderName: "A", HeaderValue: "v"}).(Webhook)
	if w.HeaderValue != "v" {
		t.Error("webhook header value not kept")
	}
	w = Webhook{URL: "https://x", HeaderName: "B"}.WithSecretsFrom(Webhook{HeaderName: "A", HeaderValue: "v"}).(Webhook)
	if w.HeaderValue != "" {
		t.Error("a renamed header must not inherit the old value")
	}
	if got := (Telegram{}).WithSecretsFrom(Discord{WebhookURL: "x"}); got.(Telegram).BotToken != "" {
		t.Error("a config of another type must be ignored")
	}
}

func TestFromValues(t *testing.T) {
	c := FromValues(TypeSMTP, values("host", " smtp.x.com ", "port", "465", "security", "tls", "username", "u", "password", " p ",
		"from", "a@b.co", "to", "x@y.io, z@y.io;\n w@y.io ,"))
	s := c.(SMTP)
	if s.Host != "smtp.x.com" || s.Port != 465 || s.Security != "tls" || len(s.To) != 3 || s.To[2] != "w@y.io" {
		t.Errorf("smtp = %#v", s)
	}
	if s.Password != " p " {
		t.Errorf("a password must not be trimmed, got %q", s.Password.Reveal())
	}
	if p := FromValues(TypeSMTP, values("port", "abc")).(SMTP).Port; p != 0 {
		t.Errorf("a bad port = %d, want 0", p)
	}
	if got := FromValues(TypeTelegram, values("bot_token", " 1:a ", "chat_id", " 5 ")).(Telegram); got.BotToken != "1:a" || got.ChatID != "5" {
		t.Errorf("telegram = %#v", got)
	}
	if got := FromValues(TypeDiscord, values("webhook_url", "https://d/x")).(Discord); got.WebhookURL != secret.String("https://d/x") {
		t.Errorf("discord = %#v", got)
	}
	if got := FromValues(TypeWebhook, values("url", "https://x", "header_name", "A", "header_value", "v")).(Webhook); got.HeaderValue != "v" {
		t.Errorf("webhook = %#v", got)
	}
	if FromValues("sms", values()) != nil {
		t.Error("an unknown type must give nil")
	}
}
