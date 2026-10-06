package notify

import (
	"context"
	"fmt"
)

// Send delivers a message through a channel of any type, with that type's
// sender. One call is one bounded attempt; retries belong to the caller.
func Send(ctx context.Context, c Config, m Message) error {
	switch c := c.(type) {
	case SMTP:
		return SendEmail(ctx, c, m)
	case Telegram:
		return SendTelegram(ctx, c, m)
	case Discord:
		return SendDiscord(ctx, c, m)
	case Webhook:
		return SendWebhook(ctx, c, m)
	}
	return fmt.Errorf("notify: unknown channel type %T", c)
}
