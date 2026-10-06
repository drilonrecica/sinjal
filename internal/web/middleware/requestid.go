// Package middleware holds the HTTP middleware shared by every route.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type requestIDKey struct{}

// RequestID assigns every request a fresh random ID, exposes it in the
// X-Request-Id response header and stores it in the request context. An
// inbound X-Request-Id is ignored: it is client-controlled and would let a
// caller forge log fields.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [8]byte
		_, _ = rand.Read(b[:]) // never fails on supported platforms
		id := hex.EncodeToString(b[:])
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// GetRequestID returns the request ID stored by RequestID, or "".
func GetRequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
