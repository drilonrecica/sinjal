package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// telegramAPI is the Bot API; tests point sendTelegram elsewhere.
const telegramAPI = "https://api.telegram.org"

// telegramLimit is the longest text sendMessage takes, in characters.
const telegramLimit = 4096

// SendTelegram posts a message with the Bot API's sendMessage. The text is
// plain: no parse mode is set, so a name or reason is never read as
// formatting, and link previews are off. The error says what Telegram
// answered; it never holds the bot token, which is part of the URL.
func SendTelegram(ctx context.Context, c Telegram, m Message) error {
	return sendTelegram(ctx, telegramAPI, c, m)
}

func sendTelegram(ctx context.Context, api string, c Telegram, m Message) error {
	body, err := json.Marshal(map[string]any{
		"chat_id":                  c.ChatID,
		"text":                     truncate(m.Telegram(), telegramLimit),
		"disable_web_page_preview": true,
	})
	if err != nil {
		return err
	}
	target := api + "/bot" + url.PathEscape(c.BotToken.Reveal()) + "/sendMessage"
	status, _, answer, err := post(ctx, httpClient, target, http.Header{}, body)
	if err != nil {
		return wrap("telegram", err)
	}
	var reply struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(answer, &reply)
	if status == http.StatusOK && reply.OK {
		return nil
	}
	return statusError("telegram", status, reply.Description)
}
