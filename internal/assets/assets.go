// Package assets serves the embedded static files under content-hashed URLs.
//
// Every file gets a URL containing a hash of its content, so it can be cached
// forever (`immutable`) and a changed file automatically gets a new URL.
// Bodies are read, hashed and (for text types) gzip-compressed once at
// startup; serving is a map lookup.
package assets

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/drilonrecica/sinjal/web"
)

// Prefix is the URL path under which assets are served.
const Prefix = "/static/"

type asset struct {
	body        []byte
	gzipped     []byte // nil when not compressible or not smaller
	contentType string
	etag        string // identity representation
	gzipETag    string // representations of one URL need distinct ETags
}

// Registry maps logical names ("css/base.css") to hashed URLs and serves them.
type Registry struct {
	byName map[string]string // logical name -> hashed name
	byHash map[string]*asset // hashed name  -> asset
}

// contentTypes is explicit so behaviour does not depend on the host mime
// database. compressible marks types worth gzipping.
var contentTypes = map[string]struct {
	mime         string
	compressible bool
}{
	".css":   {"text/css; charset=utf-8", true},
	".js":    {"text/javascript; charset=utf-8", true},
	".json":  {"application/json", true},
	".svg":   {"image/svg+xml", true},
	".txt":   {"text/plain; charset=utf-8", true},
	".woff2": {"font/woff2", false},
	".png":   {"image/png", false},
	".jpg":   {"image/jpeg", false},
	".webp":  {"image/webp", false},
	".ico":   {"image/x-icon", false},
}

// New reads every file in fsys and builds a Registry. Stylesheets are
// added last: a relative url() in one (a font, an image) is rewritten to the
// referenced asset's hashed name first, so the stylesheet's own hash changes
// whenever something it references does.
func New(fsys fs.FS) (*Registry, error) {
	r := &Registry{byName: map[string]string{}, byHash: map[string]*asset{}}
	var sheets []string
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if _, ok := contentTypes[path.Ext(name)]; !ok {
			return fmt.Errorf("asset %q: unsupported file type %q", name, path.Ext(name))
		}
		if path.Ext(name) == ".css" {
			sheets = append(sheets, name)
			return nil
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		r.add(name, body)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, name := range sheets {
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		if body, err = r.rewriteURLs(name, body); err != nil {
			return nil, err
		}
		r.add(name, body)
	}
	return r, nil
}

// add stores one file under its content-hashed name.
func (r *Registry) add(name string, body []byte) {
	ext := path.Ext(name)
	sum := sha256.Sum256(body)
	full := hex.EncodeToString(sum[:])
	hashed := strings.TrimSuffix(name, ext) + "." + full[:16] + ext
	a := &asset{
		body:        body,
		contentType: contentTypes[ext].mime,
		etag:        `"` + full + `"`,
		gzipETag:    `"` + full + `-gzip"`,
	}
	if contentTypes[ext].compressible {
		a.gzipped = compress(body)
	}
	r.byName[name] = hashed
	r.byHash[hashed] = a
}

// cssURL matches url(...) with an optional quote. Only relative references
// are rewritten; data:, absolute and fragment URLs are left alone.
var cssURL = regexp.MustCompile(`url\(\s*(["']?)([^"')]+)(["']?)\s*\)`)

// rewriteURLs points every relative url() in the stylesheet name at the
// hashed asset it references. A reference to a missing asset is an error,
// like an unknown name passed to URL.
func (r *Registry) rewriteURLs(name string, body []byte) ([]byte, error) {
	var missing error
	out := cssURL.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := cssURL.FindSubmatch(m)
		ref := string(sub[2])
		if strings.Contains(ref, ":") || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "#") {
			return m
		}
		hashed, ok := r.byName[path.Join(path.Dir(name), ref)]
		if !ok {
			missing = fmt.Errorf("asset %q: url(%s) references a missing asset", name, ref)
			return m
		}
		return []byte("url(" + string(sub[1]) + path.Join(path.Dir(ref), path.Base(hashed)) + string(sub[3]) + ")")
	})
	return out, missing
}

// compress returns the gzip form of body, or nil when it is not smaller.
func compress(body []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression) // level is valid
	zw.Write(body)
	zw.Close()
	if buf.Len() >= len(body) {
		return nil
	}
	return buf.Bytes()
}

// URL returns the hashed URL for a logical asset name such as "css/base.css".
// An unknown name is a programmer error and panics; tests render every
// template, so it cannot reach production.
func (r *Registry) URL(name string) string {
	hashed, ok := r.byName[name]
	if !ok {
		panic("assets: unknown asset " + name)
	}
	return Prefix + hashed
}

// Handler serves hashed asset URLs (everything after Prefix).
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		a, ok := r.byHash[strings.TrimPrefix(req.URL.Path, Prefix)]
		if !ok {
			http.NotFound(w, req)
			return
		}
		h := w.Header()
		h.Set("Content-Type", a.contentType)
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
		h.Set("X-Content-Type-Options", "nosniff")

		body, etag := a.body, a.etag
		if a.gzipped != nil {
			h.Add("Vary", "Accept-Encoding")
			if acceptsGzip(req) {
				h.Set("Content-Encoding", "gzip")
				body, etag = a.gzipped, a.gzipETag
			}
		}
		h.Set("ETag", etag)
		// ServeContent handles If-None-Match (304), HEAD and Content-Length.
		http.ServeContent(w, req, "", time.Time{}, bytes.NewReader(body))
	})
}

func acceptsGzip(req *http.Request) bool {
	for _, part := range strings.Split(req.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			q := strings.ReplaceAll(strings.TrimSpace(params), " ", "")
			return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
		}
	}
	return false
}

// Default serves the files embedded in the binary.
var Default = func() *Registry {
	r, err := New(web.Static)
	if err != nil {
		panic(err) // the embedded tree is fixed at build time; tests cover it
	}
	return r
}()

// URL returns the hashed URL of an embedded asset; see Registry.URL.
func URL(name string) string { return Default.URL(name) }
