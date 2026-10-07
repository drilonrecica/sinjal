package statuspage

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pngOf(w, h int) []byte {
	var b bytes.Buffer
	img := image.NewGray(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.Gray{200})
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func jpegOf(w, h int) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)), nil); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func TestCheckLogoAccepts(t *testing.T) {
	for name, c := range map[string]struct {
		data []byte
		ext  string
	}{
		"png": {pngOf(64, 32), "png"}, "jpeg": {jpegOf(64, 32), "jpg"},
		"largest png": {pngOf(1024, 1024), "png"}, "one pixel": {pngOf(1, 1), "png"},
	} {
		if ext, err := CheckLogo(c.data); err != nil || ext != c.ext {
			t.Errorf("%s: %q, %v; want %q", name, ext, err, c.ext)
		}
	}
}

func TestCheckLogoRefuses(t *testing.T) {
	var g bytes.Buffer
	if err := gif.Encode(&g, image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	huge := append(pngOf(8, 8), bytes.Repeat([]byte{0}, MaxLogoBytes)...)
	truncated := pngOf(64, 64)[:30] // a PNG signature and a cut-off header
	for name, c := range map[string]struct {
		data []byte
		msg  string
	}{
		"empty":             {nil, "empty"},
		"svg":               {[]byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`), "PNG or JPEG"},
		"svg with xml head": {[]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`), "PNG or JPEG"},
		"html":              {[]byte(`<html><script>alert(1)</script>`), "PNG or JPEG"},
		"gif":               {g.Bytes(), "PNG or JPEG"},
		"webp":              {append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 40)...), "PNG or JPEG"},
		"text":              {[]byte("hello"), "PNG or JPEG"},
		"too many bytes":    {huge, "larger than 512 KB"},
		"too wide":          {pngOf(1025, 10), "1025 × 10"},
		"too tall jpeg":     {jpegOf(10, 1025), "10 × 1025"},
		"truncated header":  {truncated, "not a valid"},
		"png magic only":    {[]byte("\x89PNG\r\n\x1a\n"), "not a valid"},
	} {
		_, err := CheckLogo(c.data)
		if !errors.Is(err, ErrLogo) || !strings.Contains(LogoMessage(err), c.msg) {
			t.Errorf("%s: %v, want a logo error containing %q", name, err, c.msg)
		}
	}
}

// A decompression bomb: a header claiming a large image is refused before
// any pixel is decoded.
func TestCheckLogoReadsOnlyTheHeader(t *testing.T) {
	data := pngOf(1024, 1024)
	if len(data) > MaxLogoBytes {
		t.Skip("encoded size is above the limit")
	}
	if _, err := CheckLogo(data[:200]); err != nil {
		t.Errorf("a PNG cut after its header is judged on the header: %v", err)
	}
}

func TestSaveAndRemoveLogo(t *testing.T) {
	dir := t.TempDir()
	data := pngOf(8, 8)
	a, err := SaveLogo(dir, data, "png")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SaveLogo(dir, data, "png")
	if a == b || !LogoNameRe.MatchString(a) || !strings.HasSuffix(a, ".png") {
		t.Errorf("names %q, %q: want distinct 128-bit hex names", a, b)
	}
	st, err := os.Stat(filepath.Join(dir, a))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("stored file: %v, %v", st, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, a)); !bytes.Equal(got, data) {
		t.Error("stored bytes differ")
	}
	if err := RemoveLogo(dir, a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, a)); !os.IsNotExist(err) {
		t.Error("logo not removed")
	}
	if err := RemoveLogo(dir, a); err != nil {
		t.Errorf("removing twice: %v", err)
	}
	// A value that is not one of ours never reaches the file system.
	keep := filepath.Join(dir, "keep.txt")
	os.WriteFile(keep, []byte("x"), 0o600)
	for _, name := range []string{"keep.txt", "../keep.txt", "", "../../etc/passwd", strings.Repeat("a", 32) + ".gif"} {
		if err := RemoveLogo(dir, name); err != nil {
			t.Errorf("RemoveLogo(%q): %v", name, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("an unexpected file name deleted another file")
	}
	if LogoContentType("x.png") != "image/png" || LogoContentType("x.jpg") != "image/jpeg" {
		t.Error("content types")
	}
}
