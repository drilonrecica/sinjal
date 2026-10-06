//go:build manual

package notify

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/secret"
)

// TestManualDiscord sends a [TEST] embed through a real webhook (docs/20
// "Manual notification verification"):
//
//	go test -tags manual -run TestManualDiscord ./internal/notify
func TestManualDiscord(t *testing.T) {
	url := os.Getenv("SINJAL_TEST_DISCORD_WEBHOOK_URL")
	if url == "" {
		t.Skip("SINJAL_TEST_DISCORD_WEBHOOK_URL not set")
	}
	c := Discord{WebhookURL: secret.String(url)}
	if errs := c.Validate(); errs != nil {
		t.Fatalf("invalid configuration: %v", errs)
	}
	e := Event{Kind: KindDown, Test: true, MonitorName: "Sinjal manual test", MonitorType: "http",
		At: time.Now(), Reason: "manual Discord check", Attempts: 1}
	if err := SendDiscord(context.Background(), c, Render(e, time.Local)); err != nil {
		t.Fatal(err)
	}
}
