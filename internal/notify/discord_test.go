package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/secret"
)

const dcToken = "abc123-webhook-token"

// fakeDiscord answers each call with the next reply; the last one repeats.
type dcReply struct {
	status int
	header map[string]string
	body   string
}

func fakeDiscord(t *testing.T, replies ...dcReply) (Discord, *[][]byte) {
	t.Helper()
	var bodies [][]byte
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, raw)
		i := int(n.Add(1)) - 1
		if i >= len(replies) {
			i = len(replies) - 1
		}
		rp := replies[i]
		for k, v := range rp.header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(rp.status)
		io.WriteString(w, rp.body)
	}))
	t.Cleanup(srv.Close)
	return Discord{WebhookURL: secret.String(srv.URL + "/api/webhooks/1/" + dcToken)}, &bodies
}

// recordSleep waits for nothing and records what it was asked to wait.
func recordSleep(waits *[]time.Duration) func(context.Context, time.Duration) error {
	return func(_ context.Context, d time.Duration) error { *waits = append(*waits, d); return nil }
}

func TestDiscordRequest(t *testing.T) {
	c, bodies := fakeDiscord(t, dcReply{status: 204})
	m := tgMessage()
	var waits []time.Duration
	if err := sendDiscord(context.Background(), c, m, recordSleep(&waits)); err != nil {
		t.Fatal(err)
	}
	want, _ := m.Discord()
	if len(*bodies) != 1 || string((*bodies)[0]) != string(want) {
		t.Fatalf("body %q, want %q", *bodies, want)
	}
	var v struct {
		Mentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}
	_ = json.Unmarshal((*bodies)[0], &v)
	if v.Mentions.Parse == nil || len(v.Mentions.Parse) != 0 {
		t.Errorf("allowed_mentions.parse = %v, want empty", v.Mentions.Parse)
	}
	if len(waits) != 0 {
		t.Errorf("waited %v", waits)
	}
}

func TestDiscordAcceptsAnyTwoXX(t *testing.T) {
	c, _ := fakeDiscord(t, dcReply{status: 200, body: `{"id":"1"}`})
	if err := sendDiscord(context.Background(), c, tgMessage(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestDiscordRateLimitedOnceThenSent(t *testing.T) {
	c, bodies := fakeDiscord(t,
		dcReply{status: 429, body: `{"message":"You are being rate limited.","retry_after":1.5,"global":false}`},
		dcReply{status: 204})
	var waits []time.Duration
	if err := sendDiscord(context.Background(), c, tgMessage(), recordSleep(&waits)); err != nil {
		t.Fatal(err)
	}
	if len(*bodies) != 2 {
		t.Errorf("%d calls, want 2", len(*bodies))
	}
	if len(waits) != 1 || waits[0] != 1500*time.Millisecond {
		t.Errorf("waits %v, want [1.5s]", waits)
	}
}

func TestDiscordRetryAfterFromHeader(t *testing.T) {
	c, _ := fakeDiscord(t, dcReply{status: 429, header: map[string]string{"Retry-After": "2"}}, dcReply{status: 204})
	var waits []time.Duration
	if err := sendDiscord(context.Background(), c, tgMessage(), recordSleep(&waits)); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 || waits[0] != 2*time.Second {
		t.Errorf("waits %v, want [2s]", waits)
	}
}

func TestDiscordLongRetryAfterIsNotWaited(t *testing.T) {
	c, bodies := fakeDiscord(t, dcReply{status: 429, body: `{"retry_after":120}`})
	var waits []time.Duration
	err := sendDiscord(context.Background(), c, tgMessage(), recordSleep(&waits))
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.After != 2*time.Minute {
		t.Fatalf("error %v, want RateLimitError of 2m", err)
	}
	if len(waits) != 0 || len(*bodies) != 1 {
		t.Errorf("waited %v, %d calls: a long limit must go back to the caller", waits, len(*bodies))
	}
}

func TestDiscordSecondRateLimitIsAnError(t *testing.T) {
	c, bodies := fakeDiscord(t, dcReply{status: 429, body: `{"retry_after":0.2}`})
	var waits []time.Duration
	err := sendDiscord(context.Background(), c, tgMessage(), recordSleep(&waits))
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("error %v, want RateLimitError", err)
	}
	if len(*bodies) != 2 || len(waits) != 1 {
		t.Errorf("%d calls, waits %v: want one retry, never a loop", len(*bodies), waits)
	}
}

func TestDiscordMissingRetryAfterIsBoundedWait(t *testing.T) {
	c, _ := fakeDiscord(t, dcReply{status: 429}, dcReply{status: 204})
	var waits []time.Duration
	if err := sendDiscord(context.Background(), c, tgMessage(), recordSleep(&waits)); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 || waits[0] <= 0 || waits[0] > maxRetryWait {
		t.Errorf("waits %v", waits)
	}
}

func TestDiscordCancelDuringWait(t *testing.T) {
	c, bodies := fakeDiscord(t, dcReply{status: 429, body: `{"retry_after":4}`}, dcReply{status: 204})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := sendDiscord(ctx, c, tgMessage(), sleepContext)
	if err == nil || !strings.Contains(err.Error(), "discord:") {
		t.Fatalf("error %v", err)
	}
	if time.Since(start) > time.Second || len(*bodies) != 1 {
		t.Errorf("took %v, %d calls", time.Since(start), len(*bodies))
	}
}

func TestDiscordErrors(t *testing.T) {
	tests := []struct {
		name   string
		reply  dcReply
		want   string
		absent string
	}{
		{"deleted webhook", dcReply{status: 404, body: `{"message":"Unknown Webhook","code":10015}`}, "discord: 404 Not Found: Unknown Webhook", ""},
		{"bad body", dcReply{status: 400, body: `{"message":"Invalid Form Body"}`}, "discord: 400", ""},
		{"server error", dcReply{status: 503, body: "<html>oops"}, "discord: 503 Service Unavailable", "<html>"},
		{"redirect", dcReply{status: 307, header: map[string]string{"Location": "http://127.0.0.1:1/x"}}, "discord: 307", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := fakeDiscord(t, tt.reply)
			err := sendDiscord(context.Background(), c, tgMessage(), nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want %q", err, tt.want)
			}
			if tt.absent != "" && strings.Contains(err.Error(), tt.absent) {
				t.Errorf("error holds %q", tt.absent)
			}
			if strings.Contains(err.Error(), dcToken) || strings.Contains(err.Error(), "/api/webhooks") {
				t.Errorf("error holds the webhook URL: %q", err)
			}
		})
	}
}

func TestDiscordConnectErrorHasNoURL(t *testing.T) {
	srv := httptest.NewServer(nil)
	url := srv.URL + "/api/webhooks/1/" + dcToken
	srv.Close()
	err := sendDiscord(context.Background(), Discord{WebhookURL: secret.String(url)}, tgMessage(), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "discord: ") || strings.Contains(err.Error(), dcToken) {
		t.Fatalf("error %v", err)
	}
}

func TestRateLimitErrorText(t *testing.T) {
	err := &RateLimitError{Channel: "discord", After: 90 * time.Second}
	if got := err.Error(); got != "discord: rate limited, retry in 1m 30s" {
		t.Errorf("text %q", got)
	}
}
