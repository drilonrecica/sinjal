package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/secret"
)

const tgToken = "123456:ABC-secret-token"

type tgCall struct {
	path string
	body map[string]any
}

// fakeTelegram answers every call with status and body and records them.
func fakeTelegram(t *testing.T, status int, answer string) (*httptest.Server, *[]tgCall) {
	t.Helper()
	var calls []tgCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		c := tgCall{path: r.URL.Path}
		_ = json.Unmarshal(raw, &c.body)
		calls = append(calls, c)
		w.WriteHeader(status)
		io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func tgMessage() Message {
	return Render(Event{Kind: KindDown, MonitorName: "API", MonitorType: "http",
		At: time.Date(2026, 10, 6, 16, 42, 13, 0, time.UTC), Reason: "timeout after 5s", Attempts: 2}, time.UTC)
}

func TestTelegramRequest(t *testing.T) {
	srv, calls := fakeTelegram(t, 200, `{"ok":true}`)
	c := Telegram{BotToken: secret.String(tgToken), ChatID: "@ops"}
	m := tgMessage()
	if err := sendTelegram(context.Background(), srv.URL, c, m); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("%d calls", len(*calls))
	}
	got := (*calls)[0]
	if got.path != "/bot"+tgToken+"/sendMessage" {
		t.Errorf("path %q", got.path)
	}
	if got.body["chat_id"] != "@ops" || got.body["text"] != m.Telegram() {
		t.Errorf("body %v", got.body)
	}
	if _, ok := got.body["parse_mode"]; ok {
		t.Error("parse_mode is set: names and reasons would be read as formatting")
	}
	if got.body["disable_web_page_preview"] != true {
		t.Error("link preview not disabled")
	}
}

func TestTelegramHostileTextStaysPlain(t *testing.T) {
	srv, calls := fakeTelegram(t, 200, `{"ok":true}`)
	m := Render(Event{Kind: KindDown, MonitorName: "<b>*_x_*</b> [a](http://evil)", Reason: "`code`"}, time.UTC)
	if err := sendTelegram(context.Background(), srv.URL, Telegram{BotToken: tgToken, ChatID: "1"}, m); err != nil {
		t.Fatal(err)
	}
	if text := (*calls)[0].body["text"].(string); !strings.Contains(text, "<b>*_x_*</b> [a](http://evil)") {
		t.Errorf("text changed: %q", text)
	}
}

func TestTelegramTruncatesLongText(t *testing.T) {
	srv, calls := fakeTelegram(t, 200, `{"ok":true}`)
	m := Message{Title: "t", Lines: []string{strings.Repeat("é", 5000)}}
	if err := sendTelegram(context.Background(), srv.URL, Telegram{BotToken: tgToken, ChatID: "1"}, m); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune((*calls)[0].body["text"].(string))); n != telegramLimit {
		t.Errorf("%d characters, want %d", n, telegramLimit)
	}
}

func TestTelegramErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		answer  string
		want    string
		notWant string
	}{
		{"bad token", 401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, "telegram: 401 Unauthorized: Unauthorized", ""},
		{"chat not found", 400, `{"ok":false,"description":"Bad Request: chat not found"}`, "chat not found", ""},
		{"rate limited", 429, `{"ok":false,"description":"Too Many Requests: retry after 5"}`, "telegram: 429", ""},
		{"server error", 502, `<html>bad gateway`, "telegram: 502 Bad Gateway", "<html>"},
		{"200 but not ok", 200, `{"ok":false,"description":"odd"}`, "odd", ""},
		{"200 not json", 200, `hello`, "telegram: 200", ""},
		{"redirect", 302, ``, "telegram: 302", ""},
		{"multi-line description", 400, `{"ok":false,"description":"a\nb\r\nc"}`, "a b c", "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeTelegram(t, tt.status, tt.answer)
			err := sendTelegram(context.Background(), srv.URL, Telegram{BotToken: tgToken, ChatID: "1"}, tgMessage())
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q lacks %q", err, tt.want)
			}
			if tt.notWant != "" && strings.Contains(err.Error(), tt.notWant) {
				t.Errorf("error %q holds %q", err, tt.notWant)
			}
			if strings.Contains(err.Error(), tgToken) {
				t.Errorf("error holds the token: %q", err)
			}
		})
	}
}

func TestTelegramConnectErrorHasNoToken(t *testing.T) {
	srv := httptest.NewServer(nil)
	url := srv.URL
	srv.Close() // nothing listens any more
	err := sendTelegram(context.Background(), url, Telegram{BotToken: tgToken, ChatID: "1"}, tgMessage())
	if err == nil || !strings.HasPrefix(err.Error(), "telegram: ") {
		t.Fatalf("error %v", err)
	}
	if strings.Contains(err.Error(), tgToken) || strings.Contains(err.Error(), url) {
		t.Errorf("error holds the URL or token: %q", err)
	}
}

func TestTelegramBoundedByContext(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := sendTelegram(ctx, srv.URL, Telegram{BotToken: tgToken, ChatID: "1"}, tgMessage())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
}
