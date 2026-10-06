package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A redirect is a failed delivery: the target must never see the message
// or the configured header.
func TestPostDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	status, _, _, err := post(context.Background(), httpClient, srv.URL, http.Header{"X-Key": {"secret"}}, []byte(`{}`))
	if err != nil || status != http.StatusTemporaryRedirect {
		t.Fatalf("status %d, error %v", status, err)
	}
	if hits.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

func TestStatusErrorIsOneLine(t *testing.T) {
	err := statusError("x", 400, "a\nb\r\n"+strings.Repeat("z", 500))
	if strings.ContainsAny(err.Error(), "\r\n") || len(err.Error()) > 260 {
		t.Errorf("error %q", err)
	}
}
