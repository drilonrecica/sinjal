package statuspage

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registers the JPEG decoder for image.DecodeConfig
	_ "image/png"  // registers the PNG decoder for image.DecodeConfig
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

// Logo limits (docs/12 "Logo files", docs/38).
const (
	MaxLogoBytes  = 512 << 10
	MaxLogoPixels = 1024
)

// ErrLogo is a logo that cannot be accepted; its message is for the admin.
var ErrLogo = errors.New("logo")

func logoError(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrLogo, fmt.Sprintf(format, a...))
}

// LogoMessage is the text of an ErrLogo without its prefix.
func LogoMessage(err error) string {
	msg := err.Error()
	return msg[len(ErrLogo.Error())+2:]
}

// CheckLogo validates an uploaded image and returns the file extension to
// store it under, "png" or "jpg". The type is decided by the content, never
// by the name the browser sent: the bytes must sniff as PNG or JPEG and
// their header must agree and describe an image of at most 1024 × 1024.
// SVG and WebP are refused (docs/12). Only the header is decoded, so a
// small file claiming huge dimensions costs nothing.
func CheckLogo(data []byte) (string, error) {
	if len(data) == 0 {
		return "", logoError("The file is empty.")
	}
	if len(data) > MaxLogoBytes {
		return "", logoError("The file is larger than %d KB.", MaxLogoBytes>>10)
	}
	var ext, format string
	switch http.DetectContentType(data) {
	case "image/png":
		ext, format = "png", "png"
	case "image/jpeg":
		ext, format = "jpg", "jpeg"
	default:
		return "", logoError("Use a PNG or JPEG image. SVG and WebP are not accepted.")
	}
	cfg, got, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || got != format {
		return "", logoError("The file is not a valid PNG or JPEG image.")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxLogoPixels || cfg.Height > MaxLogoPixels {
		return "", logoError("The image is %d × %d pixels; the most allowed is %d × %d.", cfg.Width, cfg.Height, MaxLogoPixels, MaxLogoPixels)
	}
	return ext, nil
}

// LogoNameRe matches the name of a stored logo: 128 random bits in hex.
var LogoNameRe = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg)$`)

// LogoContentType is the media type a stored logo is served as.
func LogoContentType(name string) string {
	if filepath.Ext(name) == ".png" {
		return "image/png"
	}
	return "image/jpeg"
}

// SaveLogo stores a checked logo in dir under a new random name and returns
// the name. The file is created exclusively and readable by the owner only.
func SaveLogo(dir string, data []byte, ext string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	name := hex.EncodeToString(raw) + "." + ext
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return name, nil
}

// RemoveLogo deletes a stored logo; names that are not ours are ignored, so
// a bad database value can never reach another file.
func RemoveLogo(dir, name string) error {
	if !LogoNameRe.MatchString(name) {
		return nil
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
