package statuspage

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestNormalizeSlug(t *testing.T) {
	for in, want := range map[string]string{"Status": "status", " my-app ": "my-app", "a": "a", "a1-b2": "a1-b2"} {
		if got, ok := NormalizeSlug(in); !ok || got != want {
			t.Errorf("NormalizeSlug(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "-a", "a-", "a b", "a/b", "a_b", "ünï", strings.Repeat("a", 65)} {
		if _, ok := NormalizeSlug(in); ok {
			t.Errorf("NormalizeSlug(%q) accepted", in)
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"Status.Example.com": "status.example.com", " status.example.com. ": "status.example.com",
		"localhost": "localhost", "10.0.0.5": "10.0.0.5", "a-b.c1.io": "a-b.c1.io",
	} {
		if got, err := NormalizeHost(in, "sinjal.example.com"); err != nil || got != want {
			t.Errorf("NormalizeHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "https://status.example.com", "status.example.com:8080", "status.example.com/x",
		"user@status.example.com", "-bad.example.com", "a..b", "sp ace.example.com", "under_score.example.com",
		strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 130) + "com"} {
		if got, err := NormalizeHost(in, ""); err == nil {
			t.Errorf("NormalizeHost(%q) = %q, want an error", in, got)
		}
	}
	// The instance's own address, however it is written, is refused.
	for _, in := range []string{"sinjal.example.com", "SINJAL.example.com.", " Sinjal.Example.Com"} {
		if _, err := NormalizeHost(in, "sinjal.example.com"); err == nil || !strings.Contains(err.Error(), "own address") {
			t.Errorf("NormalizeHost(%q) = %v, want the own-address error", in, err)
		}
	}
}

func TestNewTokenIs128BitsOfLowercaseBase32(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		token, hash, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[a-z2-7]{26}$`).MatchString(token) {
			t.Fatalf("token %q: want 26 lowercase base32 characters (128 bits, no padding)", token)
		}
		if seen[token] {
			t.Fatal("duplicate token")
		}
		seen[token] = true
		got, ok := HashToken(token)
		if !ok || string(got) != string(hash) || len(hash) != 32 {
			t.Fatalf("HashToken(%q) does not give the stored hash", token)
		}
	}
}

func TestHashTokenRejectsWhatWasNeverIssued(t *testing.T) {
	token, _, _ := NewToken()
	for _, in := range []string{"", "short", strings.ToUpper(token), token + "a", token[:25], strings.Repeat("1", 26), token[:25] + "!"} {
		if _, ok := HashToken(in); ok {
			t.Errorf("HashToken(%q) accepted", in)
		}
	}
}

func TestNormalizeAccent(t *testing.T) {
	if got, ok := NormalizeAccent(" #3E67A8 "); !ok || got != "#3e67a8" {
		t.Errorf("got %q, %v", got, ok)
	}
	if got, ok := NormalizeAccent(""); !ok || got != "" {
		t.Errorf("empty accent = %q, %v: it means the theme's own", got, ok)
	}
	for _, in := range []string{"3e67a8", "#3e67a", "#3e67a8f", "red", "#gggggg", "rgb(1,2,3)"} {
		if _, ok := NormalizeAccent(in); ok {
			t.Errorf("NormalizeAccent(%q) accepted", in)
		}
	}
}

func TestCheckAccent(t *testing.T) {
	cases := []struct {
		accent, theme string
		ok            bool
	}{
		{"#3e67a8", "paper", true},   // the paper theme's own accent
		{"#7da2ff", "carbon", true},  // the carbon theme's own accent
		{"#f5f2eb", "paper", false},  // the page colour itself
		{"#ffff00", "paper", false},  // yellow on cream
		{"#202020", "carbon", false}, // near-black on graphite
		{"#7da2ff", "paper", false},  // a dark-theme accent on a light page
	}
	for _, c := range cases {
		if msg := CheckAccent(c.accent, c.theme); (msg == "") != c.ok {
			t.Errorf("CheckAccent(%s, %s) = %q, want ok=%v", c.accent, c.theme, msg, c.ok)
		}
	}
	if Contrast("#000000", "#ffffff") < 20.9 || Contrast("#ffffff", "#ffffff") != 1 {
		t.Error("Contrast is wrong at the extremes")
	}
}

// The surfaces used for the accent check are the themes' own: when a
// theme's colours change, this fails until the table follows.
func TestSurfacesMatchTheTokens(t *testing.T) {
	css, err := os.ReadFile("../../web/static/css/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	for theme, want := range surfaces {
		block := regexp.MustCompile(`(?s)\[data-theme="` + theme + `"\]\s*\{(.*?)\n\}`).FindStringSubmatch(string(css))
		if block == nil {
			t.Fatalf("no block for theme %s", theme)
		}
		for i, token := range []string{"bg", "surface-1"} {
			m := regexp.MustCompile(`--` + token + `:\s*(#[0-9a-f]{6})`).FindStringSubmatch(block[1])
			if m == nil || m[1] != want[i] {
				t.Errorf("theme %s --%s = %v, table has %s", theme, token, m, want[i])
			}
		}
	}
	for _, theme := range Themes {
		if _, ok := surfaces[theme]; !ok {
			t.Errorf("theme %s has no surfaces", theme)
		}
	}
}
