package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// httpTimeout bounds one HTTP delivery, whatever deadline the caller gives.
const httpTimeout = 15 * time.Second

// maxResponseBytes is all that is read of an answer: enough for an error
// description, too little to hold anything worth leaking.
const maxResponseBytes = 4 << 10

// httpClient never follows a redirect: a redirect would carry the message
// (and any configured header) to a place the owner did not configure, so a
// 3xx is a failed delivery.
var httpClient = &http.Client{
	Timeout:       httpTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// post sends a JSON body and returns the status, the headers and at most
// maxResponseBytes of the answer. The returned error never holds the URL:
// Telegram and Discord keep their token in it.
func post(ctx context.Context, client *http.Client, target string, header http.Header, body []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, errors.New("invalid URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Sinjal")
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, transportError(ctx, err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	return resp.StatusCode, resp.Header, answer, nil
}

// transportError says what went wrong with the connection without the URL.
func transportError(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), isTimeout(err):
		return errors.New("timed out")
	case ctx.Err() != nil:
		return errors.New("cancelled")
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return errors.New(oneLine(err.Error(), maxReasonRunes))
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// statusError is "<channel>: 401 Unauthorized", with the API's own
// description after it when there is one.
func statusError(channel string, status int, detail string) error {
	text := fmt.Sprintf("%s: %d %s", channel, status, http.StatusText(status))
	if detail = oneLine(detail, maxReasonRunes); detail != "" {
		text += ": " + detail
	}
	return errors.New(text)
}

// wrap puts the channel in front of a transport error.
func wrap(channel string, err error) error {
	return fmt.Errorf("%s: %w", channel, err)
}
