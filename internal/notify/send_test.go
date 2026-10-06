package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every type reaches its own sender: with a context that has already
// ended, each fails before any connection, naming its channel.
func TestSendUsesTheSenderOfTheType(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := Render(Event{Kind: KindDown, MonitorName: "API"}, nil)
	for prefix, c := range map[string]Config{
		"smtp:":     SMTP{Host: "127.0.0.1", Port: 1, Security: SecuritySTARTTLS, From: "a@b.co", To: []string{"c@d.co"}},
		"telegram:": Telegram{BotToken: "123:abc", ChatID: "1"},
		"discord:":  Discord{WebhookURL: "http://127.0.0.1:1/hook"},
		"webhook:":  Webhook{URL: "http://127.0.0.1:1/hook"},
	} {
		err := Send(ctx, c, m)
		if err == nil || !strings.HasPrefix(err.Error(), prefix) {
			t.Errorf("%T: error %v, want one starting with %q", c, err, prefix)
		}
	}
	if err := Send(ctx, nil, m); err == nil {
		t.Error("a missing configuration was accepted")
	}
}

func TestSendDelivers(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
	}))
	defer srv.Close()
	m := Render(Event{Kind: KindDown, MonitorName: "API"}, nil)
	if err := Send(context.Background(), Webhook{URL: srv.URL}, m); err != nil {
		t.Fatal(err)
	}
	if want, _ := m.Webhook(); got != string(want) {
		t.Errorf("payload = %s, want %s", got, want)
	}
}
