package web

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/statuspage"
)

// RegisterUploads mounts GET /uploads/{name}: the status page logos in dir
// (docs/12 "Logo files"). It needs no session, since a public status page
// shows its logo to everyone; the 128-bit random file name is the only way
// to ask for one, and only names of the form the store writes are looked
// up, so no path outside dir can be reached. Files are served with the
// type their extension names, never sniffed, and cached for good: a name is
// never reused.
func RegisterUploads(r chi.Router, dir string) {
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name := chi.URLParam(req, "name")
		if !statuspage.LogoNameRe.MatchString(name) {
			http.NotFound(w, req)
			return
		}
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			http.NotFound(w, req)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", statuspage.LogoContentType(name))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeContent(w, req, name, st.ModTime(), f)
	})
	r.Method(http.MethodGet, "/uploads/{name}", h)
	r.Method(http.MethodHead, "/uploads/{name}", h)
}
