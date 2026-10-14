package web

import "net/http"

// boostToPane sends the answer to a boosted request (a link or form inside
// the app shell, htmx "hx-boost", docs/03 "Layout") into the shell's pane:
// htmx swaps the page's #main for the current one and leaves the top bar,
// the monitor column and the event stream as they are. Setting this in the
// response rather than as hx-target / hx-select on the shell keeps those
// attributes from being inherited by every live fragment and link below
// it. A page without a #main (sign-in, re-authentication, a public status
// page) is loaded whole by shell.js instead.
func boostToPane(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("HX-Boosted") == "true" {
			h := w.Header()
			h.Set("HX-Retarget", "#main")
			h.Set("HX-Reselect", "#main")
			h.Set("HX-Reswap", "outerHTML")
			h.Add("Vary", "HX-Boosted")
		}
		next.ServeHTTP(w, r)
	})
}
