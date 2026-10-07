package web

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func pngBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.Gray{128})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// uploadAs posts the logo form as a signed-in user. csrf false leaves the
// token out.
func (e *appEnv) uploadAs(t *testing.T, userID, path, filename string, data []byte, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	cookie, sess := e.signIn(t, userID)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if csrf {
		mw.WriteField(CSRFFormField, e.csrf.token(sess.ID))
	}
	if data != nil {
		part, err := mw.CreateFormFile("logo", filename)
		if err != nil {
			t.Fatal(err)
		}
		part.Write(data)
	}
	mw.Close()
	r := httptest.NewRequest("POST", path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return e.serve(withCookie(r, cookie))
}

func (e *appEnv) uploadedFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(e.uploads)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

func (e *appEnv) logoOf(t *testing.T, id string) string {
	t.Helper()
	var name *string
	if err := e.db.Reader.QueryRow(`SELECT logo_path FROM status_pages WHERE id = ?`, id).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name == nil {
		return ""
	}
	return *name
}

func TestLogoUploadServeReplaceRemove(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.postAs(t, "a1", "/status-pages", pageForm("Main", "main"))
	id := e.pageID(t, "main")
	path := "/status-pages/" + id + "/logo"

	if edit := e.getAs(t, "a1", "GET", "/status-pages/"+id+"/edit").Body.String(); !strings.Contains(edit, `enctype="multipart/form-data"`) ||
		!strings.Contains(edit, `accept="image/png,image/jpeg"`) || !strings.Contains(edit, "Upload logo") {
		t.Error("the edit page has no logo form")
	}

	// The browser's file name and type do not matter: the content does.
	data := pngBytes(t, 40, 20)
	rec := e.uploadAs(t, "a1", path, "../../evil.svg", data, true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/status-pages/"+id+"/edit" {
		t.Fatalf("upload = %d %s\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	first := e.logoOf(t, id)
	if !regexp.MustCompile(`^[0-9a-f]{32}\.png$`).MatchString(first) {
		t.Fatalf("stored name = %q, want 128 random bits and the extension of the content", first)
	}
	if files := e.uploadedFiles(t); len(files) != 1 || files[0] != first {
		t.Errorf("uploads = %v", files)
	}
	if got, _ := os.ReadFile(filepath.Join(e.uploads, first)); !bytes.Equal(got, data) {
		t.Error("stored bytes differ from the upload")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'status_page.updated' AND object_id = ?`, id); n != 1 {
		t.Errorf("%d audit events", n)
	}

	// Served to anyone, as an image, never sniffed, cached.
	rec = e.serve(req("GET", "/uploads/"+first, nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "immutable") || !bytes.Equal(rec.Body.Bytes(), data) {
		t.Errorf("serve = %d %q nosniff=%q cache=%q", rec.Code, rec.Header().Get("Content-Type"),
			rec.Header().Get("X-Content-Type-Options"), rec.Header().Get("Cache-Control"))
	}
	if rec := e.serve(req("HEAD", "/uploads/"+first, nil)); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d bytes", rec.Code, rec.Body.Len())
	}
	if edit := e.getAs(t, "a1", "GET", "/status-pages/"+id+"/edit").Body.String(); !strings.Contains(edit, `src="/uploads/`+first+`"`) ||
		!strings.Contains(edit, "Replace logo") || !strings.Contains(edit, "Remove logo") {
		t.Error("the edit page does not show the logo")
	}

	// A settings save keeps the logo.
	e.postAs(t, "a1", "/status-pages/"+id, pageForm("Main", "main"))
	if e.logoOf(t, id) != first {
		t.Error("saving the settings dropped the logo")
	}

	// A replacement removes the old file.
	rec = e.uploadAs(t, "a1", path, "new.png", pngBytes(t, 10, 10), true)
	second := e.logoOf(t, id)
	if rec.Code != 303 || second == first || len(e.uploadedFiles(t)) != 1 || e.uploadedFiles(t)[0] != second {
		t.Fatalf("replace = %d, logo %q → %q, files %v", rec.Code, first, second, e.uploadedFiles(t))
	}
	if rec := e.serve(req("GET", "/uploads/"+first, nil)); rec.Code != 404 {
		t.Errorf("the replaced logo is still served: %d", rec.Code)
	}

	// Remove.
	if rec := e.postAs(t, "a1", path+"/delete", nil); rec.Code != 303 {
		t.Fatalf("remove = %d", rec.Code)
	}
	if e.logoOf(t, id) != "" || len(e.uploadedFiles(t)) != 0 {
		t.Errorf("after removing: %q, files %v", e.logoOf(t, id), e.uploadedFiles(t))
	}

	// Deleting a page deletes its logo file.
	e.uploadAs(t, "a1", path, "l.png", data, true)
	if len(e.uploadedFiles(t)) != 1 {
		t.Fatal("no logo to delete")
	}
	e.postAs(t, "a1", "/status-pages/"+id+"/delete", map[string][]string{"confirm": {"1"}})
	if files := e.uploadedFiles(t); len(files) != 0 {
		t.Errorf("a deleted page left %v", files)
	}
}

func TestLogoUploadRefusals(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "a1", "admin", "admin", "")
	e.addUser(t, "v1", "viewer", "viewer", "")
	e.postAs(t, "a1", "/status-pages", pageForm("Main", "main"))
	id := e.pageID(t, "main")
	path := "/status-pages/" + id + "/logo"

	for name, c := range map[string]struct {
		file string
		data []byte
		msg  string
	}{
		"svg":       {"logo.png", []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`), "PNG or JPEG"},
		"html":      {"logo.png", []byte("<html><script>alert(1)</script></html>"), "PNG or JPEG"},
		"too big":   {"logo.png", append(pngBytes(t, 8, 8), make([]byte, 600<<10)...), "larger than 512 KB"},
		"too wide":  {"logo.png", pngBytes(t, 1025, 4), "1025 × 4"},
		"truncated": {"logo.png", pngBytes(t, 8, 8)[:20], "not a valid"},
		"no file":   {"", nil, "Choose a PNG or JPEG"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := e.uploadAs(t, "a1", path, c.file, c.data, true)
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), c.msg) || !strings.Contains(rec.Body.String(), `href="#logo"`) {
				t.Errorf("= %d, want 422 with %q:\n%.600s", rec.Code, c.msg, rec.Body)
			}
			if e.logoOf(t, id) != "" || len(e.uploadedFiles(t)) != 0 {
				t.Errorf("a refused file was stored: %q %v", e.logoOf(t, id), e.uploadedFiles(t))
			}
		})
	}

	good := pngBytes(t, 8, 8)
	if rec := e.uploadAs(t, "v1", path, "l.png", good, true); rec.Code != http.StatusForbidden {
		t.Errorf("viewer = %d", rec.Code)
	}
	if rec := e.uploadAs(t, "a1", path, "l.png", good, false); rec.Code != http.StatusForbidden {
		t.Errorf("no CSRF token = %d, want 403", rec.Code)
	}
	if rec := e.uploadAs(t, "a1", path, "l.png", make([]byte, 5<<20), true); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("5 MiB = %d, want 413", rec.Code)
	}
	if rec := e.uploadAs(t, "a1", "/status-pages/missing/logo", "l.png", good, true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown page = %d", rec.Code)
	}
	if rec := e.postAs(t, "a1", "/status-pages/missing/logo/delete", nil); rec.Code != http.StatusNotFound {
		t.Errorf("remove on an unknown page = %d", rec.Code)
	}
	if e.logoOf(t, id) != "" || len(e.uploadedFiles(t)) != 0 {
		t.Error("a refused upload left something behind")
	}
}

func TestUploadsServeOnlyLogoNames(t *testing.T) {
	e := newAppEnv(t)
	secret := filepath.Join(e.uploads, "secret.txt")
	os.WriteFile(secret, []byte("do not serve"), 0o600)
	os.Mkdir(filepath.Join(e.uploads, strings.Repeat("a", 32)+".png"), 0o700) // a directory with a logo's name
	for _, p := range []string{
		"/uploads/secret.txt", "/uploads/../secret.txt", "/uploads/%2e%2e%2fsecret.txt", "/uploads/" + strings.Repeat("a", 32) + ".gif",
		"/uploads/" + strings.Repeat("a", 32) + ".png", "/uploads/" + strings.Repeat("b", 32) + ".png", "/uploads/",
		"/uploads/" + strings.Repeat("A", 32) + ".png",
	} {
		rec := e.serve(req("GET", p, nil))
		if rec.Code == 200 || strings.Contains(rec.Body.String(), "do not serve") {
			t.Errorf("GET %s = %d", p, rec.Code)
		}
	}
	if rec := e.serve(req("POST", "/uploads/"+strings.Repeat("b", 32)+".png", nil)); rec.Code == 200 {
		t.Errorf("POST = %d", rec.Code)
	}
}
