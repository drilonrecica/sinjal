package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/secret"
)

// Every kind of message the dispatcher and the test pages send reaches a
// fake Bot API as the plain text of its rendering (M5-12: the binary
// cannot be pointed at a fake Telegram, so the channel is covered here).
func TestTelegramDeliversEveryKind(t *testing.T) {
	srv, calls := fakeTelegram(t, 200, `{"ok":true}`)
	c := Telegram{BotToken: secret.String(tgToken), ChatID: "-100123"}
	at := time.Date(2026, 10, 6, 16, 42, 13, 0, time.UTC)
	latency := 51 * time.Millisecond
	cases := []struct {
		e    Event
		want string
	}{
		{Event{Kind: KindDown, MonitorName: "API", At: at, Reason: "timeout after 5s", Attempts: 2}, "API is DOWN\nReason: timeout after 5s"},
		{Event{Kind: KindRecovery, MonitorName: "API", At: at, Duration: 257 * time.Second, Latency: &latency}, "API recovered\nDowntime: 4m 17s\nCurrent latency: 51 ms"},
		{Event{Kind: KindReminder, MonitorName: "API", At: at, Duration: 62 * time.Minute, Reason: "connection refused"}, "API is still DOWN\nDuration: 1h 02m\nReason: connection refused"},
		{Event{Kind: KindTLSWarning, MonitorName: "Site", At: at, CertExpiry: at.Add(14 * 24 * time.Hour), DaysLeft: 14}, "Site TLS certificate expires in 14 days"},
		{Event{Kind: KindDown, Test: true, MonitorName: "Example monitor", At: at, Reason: "simulated failure"}, "[TEST] Example monitor is DOWN\nThis is a simulated Sinjal incident."},
	}
	for i, tc := range cases {
		if err := sendTelegram(context.Background(), srv.URL, c, Render(tc.e, time.UTC)); err != nil {
			t.Fatalf("%s: %v", tc.e.Kind, err)
		}
		got := (*calls)[i]
		text, _ := got.body["text"].(string)
		if !strings.HasPrefix(text, tc.want) || got.body["chat_id"] != "-100123" {
			t.Errorf("%s: sent %v, want text starting %q", tc.e.Kind, got.body, tc.want)
		}
	}
}
