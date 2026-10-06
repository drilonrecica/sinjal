//go:build manual

package notify

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/secret"
)

// TestManualTelegram sends a [TEST] message through the real Bot API (docs/20
// "Manual notification verification"):
//
//	go test -tags manual -run TestManualTelegram ./internal/notify
func TestManualTelegram(t *testing.T) {
	token, chat := os.Getenv("SINJAL_TEST_TELEGRAM_BOT_TOKEN"), os.Getenv("SINJAL_TEST_TELEGRAM_CHAT_ID")
	if token == "" || chat == "" {
		t.Skip("SINJAL_TEST_TELEGRAM_* not set")
	}
	c := Telegram{BotToken: secret.String(token), ChatID: chat}
	if errs := c.Validate(); errs != nil {
		t.Fatalf("invalid configuration: %v", errs)
	}
	e := Event{Kind: KindDown, Test: true, MonitorName: "Sinjal manual test", MonitorType: "http",
		At: time.Now(), Reason: "manual Telegram check", Attempts: 1}
	if err := SendTelegram(context.Background(), c, Render(e, time.Local)); err != nil {
		t.Fatal(err)
	}
}
