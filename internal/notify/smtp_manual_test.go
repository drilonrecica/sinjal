//go:build manual

package notify

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/secret"
)

// TestManualSMTP sends a [TEST] message through a real server (docs/20
// "Manual notification verification"):
//
//	go test -tags manual -run TestManualSMTP ./internal/notify
//
// SINJAL_TEST_SMTP_SECURITY is "starttls" (default) or "tls".
func TestManualSMTP(t *testing.T) {
	host := os.Getenv("SINJAL_TEST_SMTP_HOST")
	if host == "" {
		t.Skip("SINJAL_TEST_SMTP_* not set")
	}
	port, _ := strconv.Atoi(os.Getenv("SINJAL_TEST_SMTP_PORT"))
	security := os.Getenv("SINJAL_TEST_SMTP_SECURITY")
	if security == "" {
		security = SecuritySTARTTLS
	}
	c := SMTP{Host: host, Port: port, Security: security,
		Username: os.Getenv("SINJAL_TEST_SMTP_USER"), Password: secret.String(os.Getenv("SINJAL_TEST_SMTP_PASS")),
		From: os.Getenv("SINJAL_TEST_SMTP_FROM"), To: strings.Split(os.Getenv("SINJAL_TEST_SMTP_TO"), ",")}
	if errs := c.Validate(); errs != nil {
		t.Fatalf("invalid configuration: %v", errs)
	}
	e := Event{Kind: KindDown, Test: true, MonitorName: "Sinjal manual test", MonitorType: "http",
		At: time.Now(), Reason: "manual SMTP check", Attempts: 1}
	if err := SendEmail(context.Background(), c, Render(e, time.Local)); err != nil {
		t.Fatal(err)
	}
}
