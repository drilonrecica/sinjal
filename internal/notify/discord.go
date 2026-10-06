package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// maxRetryWait is the longest a sender waits out a rate limit itself. A
// longer one goes back to the caller as a RateLimitError, which has its own
// retry schedule; a sender must not hold a worker for minutes.
const maxRetryWait = 5 * time.Second

// defaultRetryWait is used when a 429 names no wait.
const defaultRetryWait = time.Second

// RateLimitError is a channel asking to be called again later. The sender
// has already waited once if the wait was short.
type RateLimitError struct {
	Channel string
	After   time.Duration
}

func (e *RateLimitError) Error() string {
	return e.Channel + ": rate limited, retry in " + FormatDuration(e.After)
}

// SendDiscord posts one embed to a channel webhook. A 429 whose wait is at
// most maxRetryWait is waited out and retried once; a longer wait, or a
// second 429, is a RateLimitError. The webhook URL carries a token, so no
// error ever includes it.
func SendDiscord(ctx context.Context, c Discord, m Message) error {
	return sendDiscord(ctx, c, m, sleepContext)
}

// sendDiscord is SendDiscord with the wait injectable.
func sendDiscord(ctx context.Context, c Discord, m Message, sleep func(context.Context, time.Duration) error) error {
	body, err := m.Discord()
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		status, header, answer, err := post(ctx, httpClient, c.WebhookURL.Reveal(), http.Header{}, body)
		if err != nil {
			return wrap("discord", err)
		}
		if status/100 == 2 {
			return nil
		}
		var reply struct {
			Message    string  `json:"message"`
			RetryAfter float64 `json:"retry_after"`
		}
		_ = json.Unmarshal(answer, &reply)
		if status != http.StatusTooManyRequests {
			return statusError("discord", status, reply.Message)
		}
		after := retryAfter(reply.RetryAfter, header)
		if attempt > 0 || after > maxRetryWait {
			return &RateLimitError{Channel: "discord", After: after}
		}
		if err := sleep(ctx, after); err != nil {
			return wrap("discord", transportError(ctx, err))
		}
	}
}

// retryAfter is the wait a 429 asks for: the JSON body's seconds, else the
// Retry-After header, else a second.
func retryAfter(bodySeconds float64, h http.Header) time.Duration {
	s := bodySeconds
	if s <= 0 {
		s, _ = strconv.ParseFloat(h.Get("Retry-After"), 64)
	}
	switch {
	case s <= 0:
		return defaultRetryWait
	case s > 86400:
		s = 86400 // a day: the caller decides what to do with it
	}
	return time.Duration(s * float64(time.Second))
}

// sleepContext waits d or until ctx ends.
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
