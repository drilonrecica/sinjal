package web

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// publicSheets are the status page stylesheets: the shared chrome and one
// per layout (docs/12 "Layouts").
var publicSheets = []string{"public", "public_signal", "public_sentence", "public_heartline", "public_logbook"}

var reducedMotionRe = regexp.MustCompile(`@media \(prefers-reduced-motion: reduce\)\s*\{`)

// splitReducedMotion returns css without its reduced-motion blocks, and
// those blocks' contents.
func splitReducedMotion(css string) (rest, reduced string) {
	for {
		loc := reducedMotionRe.FindStringIndex(css)
		if loc == nil {
			return rest + css, reduced
		}
		depth, end := 1, loc[1]
		for ; depth > 0 && end < len(css); end++ {
			switch css[end] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		rest += css[:loc[0]]
		reduced += css[loc[1] : end-1]
		css = css[end:]
	}
}

// Every animation of a status page mark stops under reduced motion (docs/04
// "Status page marks"): base.css cuts every animation short, and each
// layout's own rule also stops or hides each animated element, so none
// rests mid-way.
func TestPublicAnimationsStopUnderReducedMotion(t *testing.T) {
	animated := 0
	for _, name := range publicSheets {
		raw, err := fs.ReadFile(Static, "css/"+name+".css")
		if err != nil {
			t.Fatal(err)
		}
		rest, reduced := splitReducedMotion(commentRe.ReplaceAllString(string(raw), ""))
		stopped := map[string]bool{}
		for _, m := range blockRe.FindAllStringSubmatch(reduced, -1) {
			if !strings.Contains(m[2], "animation: none") && !strings.Contains(m[2], "display: none") {
				continue
			}
			for _, sel := range strings.Split(m[1], ",") {
				stopped[strings.TrimSpace(sel)] = true
			}
		}
		if strings.Contains(rest, "transition") {
			t.Errorf("%s has a transition; status page marks move by animation only", name)
		}
		for _, m := range blockRe.FindAllStringSubmatch(rest, -1) {
			for _, p := range propRe.FindAllStringSubmatch(m[2], -1) {
				if p[1] != "animation" || strings.TrimSpace(p[2]) == "none" {
					continue
				}
				for _, sel := range strings.Split(m[1], ",") {
					animated++
					if sel = strings.TrimSpace(sel); !stopped[sel] {
						t.Errorf("%s: %s animates and does not stop under reduced motion", name, sel)
					}
				}
			}
		}
	}
	// The lamp's halo, the live dot's ring and the scan line.
	if animated != 3 {
		t.Errorf("%d animated elements, want the 3 of docs/04", animated)
	}
}

// Each layout's sheet stays under 6 KB gzip -9 (docs/18 "Status page
// layouts").
func TestPublicLayoutSheetsSize(t *testing.T) {
	for _, name := range publicSheets[1:] {
		raw, err := fs.ReadFile(Static, "css/"+name+".css")
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		zw.Write(raw)
		zw.Close()
		if buf.Len() >= 6*1024 {
			t.Errorf("css/%s.css is %d bytes gzip -9, over the 6 KB of a layout sheet", name, buf.Len())
		}
	}
}
