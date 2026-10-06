package notify

import (
	"context"
	"net/http"
)

// SendWebhook posts the JSON payload of Message.Webhook() (docs/36) to the
// configured URL, with the optional extra header (an Authorization header,
// say). Any 2xx is success; everything else, a redirect included, is a
// failure. The error holds the channel, the status and nothing else: not
// the URL (it may carry a token), not the header value, and never the
// answer, which can be an internal diagnostic page.
func SendWebhook(ctx context.Context, c Webhook, m Message) error {
	return sendWebhook(ctx, httpClient, c, m)
}

func sendWebhook(ctx context.Context, client *http.Client, c Webhook, m Message) error {
	body, err := m.Webhook()
	if err != nil {
		return err
	}
	header := http.Header{}
	if c.HeaderName != "" {
		header.Set(c.HeaderName, c.HeaderValue.Reveal())
	}
	status, _, _, err := post(ctx, client, c.URL, header, body)
	if err != nil {
		return wrap("webhook", err)
	}
	if status/100 != 2 {
		return statusError("webhook", status, "")
	}
	return nil
}
