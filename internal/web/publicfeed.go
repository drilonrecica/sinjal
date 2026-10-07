package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/statuspage"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/web/proxy"
	"github.com/drilonrecica/sinjal/web/templates"
)

// The machine-readable forms of a status page (docs/12 "JSON endpoint",
// "RSS/Atom"). They are built from the same cached view as the HTML page,
// so they show exactly what the page shows, and they pass through the same
// access code (Public.serve): a feed never opens a page the page itself
// would keep closed.

var keyEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// opaqueKey names a component or incident to readers of a page without
// revealing its id: a keyed hash, stable for the page, different on every
// page and not reversible. kind keeps the two namespaces apart.
func (h *Public) opaqueKey(pageID, kind, id string) string {
	m := hmac.New(sha256.New, h.key)
	m.Write([]byte("feed\x00" + kind + "\x00" + pageID + "\x00" + id))
	return strings.ToLower(keyEncoding.EncodeToString(m.Sum(nil)))[:16]
}

type apiPage struct {
	Page struct {
		Title       string `json:"title"`
		Description string `json:"description,omitempty"`
	} `json:"page"`
	OverallStatus struct {
		State string `json:"state"`
		Text  string `json:"text"`
	} `json:"overall_status"`
	GeneratedAt string         `json:"generated_at"`
	Uptime90d   string         `json:"uptime_90d"`
	Components  []apiComponent `json:"components"`
	Incidents   []apiIncident  `json:"incidents"`
}

type apiComponent struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Group     string `json:"group,omitempty"`
	Status    string `json:"status"`
	LatencyMS *int64 `json:"latency_ms,omitempty"`
	Uptime90d string `json:"uptime_90d"`
}

type apiIncident struct {
	Key       string    `json:"key"`
	Component string    `json:"component"`
	Active    bool      `json:"active"`
	StartedAt string    `json:"started_at"`
	EndedAt   string    `json:"ended_at,omitempty"`
	Notes     []apiNote `json:"notes"`
}

type apiNote struct {
	At      string `json:"at"`
	Message string `json:"message"`
}

func apiOf(v templates.PublicPage) apiPage {
	var a apiPage
	a.Page.Title, a.Page.Description = v.Title, v.Description
	a.OverallStatus.State, a.OverallStatus.Text = v.Overall, v.OverallText
	a.GeneratedAt, a.Uptime90d = v.GeneratedAt, v.Uptime
	a.Components, a.Incidents = []apiComponent{}, []apiIncident{}
	keys := map[string]string{} // public name -> component key, for incidents
	for _, g := range v.Groups {
		for _, r := range g.Rows {
			c := apiComponent{Key: r.Key, Name: r.Name, Group: g.Name, Status: r.State, Uptime90d: r.Uptime}
			if r.HasLatency {
				ms := r.LatencyMS
				c.LatencyMS = &ms
			}
			a.Components = append(a.Components, c)
			keys[r.Name] = r.Key
		}
	}
	for _, in := range v.Incidents {
		i := apiIncident{Key: in.Key, Component: keys[in.Service], Active: in.Active, StartedAt: in.StartedAt, EndedAt: in.EndedAt, Notes: []apiNote{}}
		for _, n := range in.Notes {
			i.Notes = append(i.Notes, apiNote{At: n.At, Message: n.Message})
		}
		a.Incidents = append(a.Incidents, i)
	}
	return a
}

// writeJSON serves api.json.
func (h *Public) writeJSON(w http.ResponseWriter, r *http.Request, p store.StatusPageDetail) {
	v, err := h.view(r.Context(), p)
	if err != nil {
		h.fail(w, r, "building a status page", err)
		return
	}
	body, err := json.Marshal(apiOf(v))
	if err != nil {
		h.fail(w, r, "encoding a status page", err)
		return
	}
	machineHeaders(w, "application/json")
	w.Write(body)
}

func machineHeaders(w http.ResponseWriter, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	Sub     string      `xml:"subtitle,omitempty"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Link    atomLink    `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr,omitempty"`
	Href string `xml:"href,attr"`
}

type atomEntry struct {
	Title   string   `xml:"title"`
	ID      string   `xml:"id"`
	Updated string   `xml:"updated"`
	Content atomText `xml:"content"`
}

type atomText struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

// writeFeed serves feed.xml: an Atom feed with one entry per incident of
// the page, as listed on the page itself.
func (h *Public) writeFeed(w http.ResponseWriter, r *http.Request, p store.StatusPageDetail, at pageAccess) {
	v, err := h.view(r.Context(), p)
	if err != nil {
		h.fail(w, r, "building a status page", err)
		return
	}
	scheme := "http"
	if proxy.IsHTTPS(r) {
		scheme = "https"
	}
	f := atomFeed{Title: v.Title + " incidents", Sub: v.Description, Updated: v.GeneratedAt,
		ID:   "urn:sinjal:feed:" + h.opaqueKey(p.ID, "page", p.ID),
		Link: atomLink{Rel: "self", Href: scheme + "://" + proxy.Host(r) + r.URL.Path}}
	for _, in := range v.Incidents {
		state, updated := "ongoing", in.StartedAt
		if !in.Active {
			state, updated = "resolved", in.EndedAt
		}
		var b strings.Builder
		b.WriteString(in.Service + " has been down since " + in.StartedAt)
		if in.Active {
			b.WriteString(" (" + in.Duration + " so far).")
		} else {
			b.WriteString(" and recovered at " + in.EndedAt + " (" + in.Duration + ").")
		}
		for _, n := range in.Notes {
			b.WriteString("\n" + n.At + ": " + n.Message)
			if n.At > updated {
				updated = n.At
			}
		}
		if updated > f.Updated {
			f.Updated = updated
		}
		f.Entries = append(f.Entries, atomEntry{Title: in.Service + ": " + state,
			ID: "urn:sinjal:incident:" + in.Key + ":" + state, Updated: updated, Content: atomText{Type: "text", Text: b.String()}})
	}
	machineHeaders(w, "application/atom+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", " ")
	if err := enc.Encode(f); err != nil {
		h.log.Error("writing a feed failed", "error", err)
	}
}

// bySlugAs and byTokenAs serve api.json and feed.xml of /status/{slug} and
// /s/{token}.
func (h *Public) bySlugAs(kind int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug, ok := statuspage.NormalizeSlug(chi.URLParam(r, "slug"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		p, ok := h.lookup(w, r, func() (store.StatusPageDetail, error) {
			return store.GetStatusPageBySlug(r.Context(), h.db.Reader, slug)
		})
		if !ok || p.Visibility == statuspage.Unlisted {
			if ok {
				http.NotFound(w, r)
			}
			return
		}
		h.serve(w, r, p, pageAccess{base: "/status/" + slug, kind: kind})
	}
}

func (h *Public) byTokenAs(kind int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "token")
		hash, ok := statuspage.HashToken(token)
		if !ok {
			http.NotFound(w, r)
			return
		}
		p, ok := h.lookup(w, r, func() (store.StatusPageDetail, error) {
			return store.GetStatusPageByTokenHash(r.Context(), h.db.Reader, hash)
		})
		if ok {
			h.serve(w, r, p, pageAccess{base: "/s/" + token, kind: kind})
		}
	}
}

func (h *Public) byHostAs(kind int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := r.Context().Value(hostPageKey{}).(string)
		p, ok := h.lookup(w, r, func() (store.StatusPageDetail, error) {
			return store.GetStatusPage(r.Context(), h.db.Reader, id)
		})
		if ok {
			h.serve(w, r, p, pageAccess{base: "/", mapped: true, kind: kind})
		}
	}
}

// lookup runs a page lookup and answers 404 or 500 itself; ok is false
// when it did.
func (h *Public) lookup(w http.ResponseWriter, r *http.Request, get func() (store.StatusPageDetail, error)) (store.StatusPageDetail, bool) {
	p, err := get()
	switch {
	case err == nil:
		return p, true
	case isNotFound(err):
		http.NotFound(w, r)
	default:
		h.fail(w, r, "loading a status page", err)
	}
	return p, false
}
