package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover turns a handler panic into a logged 500 so one bad request cannot
// take the process down. http.ErrAbortHandler keeps its net/http meaning
// (abort the response silently) and is re-panicked.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				log.Error("panic recovered",
					"request_id", GetRequestID(r.Context()),
					"method", r.Method,
					"route", routePattern(r),
					"panic", fmt.Sprint(rec),
					"stack", string(debug.Stack()),
				)
				if sw, ok := w.(*statusWriter); ok && sw.wrote {
					return // response already started; nothing sensible to add
				}
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}()
			next.ServeHTTP(w, r)
		})
	}
}
