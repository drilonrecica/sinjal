package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/secret"
)

const whSecret = "Bearer s3cr3t-header-value"

type whCall struct {
	method string
	header http.Header
	body   []byte
}

func fakeWebhook(t *testing.T, status int, answer string) (*httptest.Server, *[]whCall) {
	t.Helper()
	var calls []whCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls = append(calls, whCall{r.Method, r.Header.Clone(), raw})
		w.WriteHeader(status)
		io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestWebhookRequest(t *testing.T) {
	srv, calls := fakeWebhook(t, 200, "")
	m := tgMessage()
	c := Webhook{URL: srv.URL + "/hook", HeaderName: "Authorization", HeaderValue: secret.String(whSecret)}
	if err := SendWebhook(context.Background(), c, m); err != nil {
		t.Fatal(err)
	}
	got := (*calls)[0]
	want, _ := m.Webhook()
	if got.method != "POST" || string(got.body) != string(want) {
		t.Errorf("%s %q, want the payload of Message.Webhook()", got.method, got.body)
	}
	if got.header.Get("Content-Type") != "application/json" || got.header.Get("User-Agent") != "Sinjal" {
		t.Errorf("headers %v", got.header)
	}
	if got.header.Get("Authorization") != whSecret {
		t.Errorf("extra header %q", got.header.Get("Authorization"))
	}
}

func TestWebhookWithoutExtraHeader(t *testing.T) {
	srv, calls := fakeWebhook(t, 204, "")
	if err := SendWebhook(context.Background(), Webhook{URL: srv.URL}, tgMessage()); err != nil {
		t.Fatal(err)
	}
	if h := (*calls)[0].header; h.Get("Authorization") != "" || len(h) > 6 {
		t.Errorf("headers %v", h)
	}
}

func TestWebhookTestEventSaysSo(t *testing.T) {
	srv, calls := fakeWebhook(t, 200, "")
	m := Render(Event{Kind: KindDown, Test: true, MonitorName: "API"}, time.UTC)
	if err := SendWebhook(context.Background(), Webhook{URL: srv.URL}, m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string((*calls)[0].body), `"test": true`) {
		t.Errorf("body %s", (*calls)[0].body)
	}
}

func TestWebhookStatuses(t *testing.T) {
	for _, tt := range []struct {
		status int
		ok     bool
	}{{200, true}, {201, true}, {202, true}, {204, true}, {299, true},
		{301, false}, {302, false}, {400, false}, {401, false}, {404, false}, {429, false}, {500, false}, {503, false}} {
		srv, _ := fakeWebhook(t, tt.status, "")
		err := SendWebhook(context.Background(), Webhook{URL: srv.URL}, tgMessage())
		if (err == nil) != tt.ok {
			t.Errorf("status %d: error %v", tt.status, err)
		}
	}
}

// The answer of the endpoint (a stack trace, an internal address, an
// echoed header) is never part of an error, and neither is the header
// value.
func TestWebhookErrorHasNoBodyNoSecret(t *testing.T) {
	body := "Traceback: internal host db-7.corp:5432 " + whSecret
	srv, _ := fakeWebhook(t, 500, body)
	c := Webhook{URL: srv.URL + "/hook?token=urltoken", HeaderName: "Authorization", HeaderValue: secret.String(whSecret)}
	err := SendWebhook(context.Background(), c, tgMessage())
	if err == nil || err.Error() != "webhook: 500 Internal Server Error" {
		t.Fatalf("error %v", err)
	}
}

func TestWebhookConnectErrorHasNoURL(t *testing.T) {
	srv := httptest.NewServer(nil)
	url := srv.URL + "/hook?token=urltoken"
	srv.Close()
	err := SendWebhook(context.Background(), Webhook{URL: url}, tgMessage())
	if err == nil || !strings.HasPrefix(err.Error(), "webhook: ") ||
		strings.Contains(err.Error(), "urltoken") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error %v", err)
	}
}

// A redirect is a failure and carries nothing: the configured header must
// not reach the other server.
func TestWebhookRedirectNotFollowed(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	c := Webhook{URL: srv.URL, HeaderName: "X-Key", HeaderValue: secret.String(whSecret)}
	err := SendWebhook(context.Background(), c, tgMessage())
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("error %v", err)
	}
	if hits.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

func TestWebhookBoundedByContext(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := SendWebhook(ctx, Webhook{URL: srv.URL}, tgMessage())
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(start) > 2*time.Second {
		t.Fatalf("error %v after %v", err, time.Since(start))
	}
}

func TestWebhookHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	if err := sendWebhook(context.Background(), srv.Client(), Webhook{URL: srv.URL}, tgMessage()); err != nil {
		t.Fatal(err)
	}
	// With the system roots the self-signed server is refused, and the
	// error is about the certificate, not the URL.
	err := SendWebhook(context.Background(), Webhook{URL: srv.URL}, tgMessage())
	if err == nil || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("error %v", err)
	}
}
