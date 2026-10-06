package history

import (
	"net/url"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	bel, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-27 12:00 UTC is two days after the change to CET.
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		q        url.Values
		from, to string // RFC 3339 UTC
		preset   string
		err      bool
	}{
		{"default", url.Values{}, "2026-10-26T12:00:00Z", "2026-10-27T12:00:00Z", "24h", false},
		{"unknown preset", url.Values{"range": {"5y"}}, "2026-10-26T12:00:00Z", "2026-10-27T12:00:00Z", "24h", false},
		{"1h", url.Values{"range": {"1h"}}, "2026-10-27T11:00:00Z", "2026-10-27T12:00:00Z", "1h", false},
		// Seven calendar days in Belgrade: 13:00 CET now, 13:00 CEST then.
		{"7d across DST", url.Values{"range": {"7d"}}, "2026-10-20T11:00:00Z", "2026-10-27T12:00:00Z", "7d", false},
		{"30d", url.Values{"range": {"30d"}}, "2026-09-27T11:00:00Z", "2026-10-27T12:00:00Z", "30d", false},
		{"90d", url.Values{"range": {"90d"}}, "2026-07-29T11:00:00Z", "2026-10-27T12:00:00Z", "90d", false},
		{"1y", url.Values{"range": {"1y"}}, "2025-10-27T12:00:00Z", "2026-10-27T12:00:00Z", "1y", false},
		{"custom", url.Values{"from": {"2026-10-24T10:00"}, "to": {"2026-10-26T10:00"}}, "2026-10-24T08:00:00Z", "2026-10-26T09:00:00Z", "", false},
		{"custom ending later ends now", url.Values{"from": {"2026-10-27T10:00"}, "to": {"2026-10-28T10:00"}}, "2026-10-27T09:00:00Z", "2026-10-27T12:00:00Z", "", false},
		{"custom backwards", url.Values{"from": {"2026-10-26T10:00"}, "to": {"2026-10-24T10:00"}}, "2026-10-26T12:00:00Z", "2026-10-27T12:00:00Z", "24h", true},
		{"custom empty", url.Values{"from": {"2026-10-26T10:00"}, "to": {""}}, "2026-10-26T12:00:00Z", "2026-10-27T12:00:00Z", "24h", true},
		{"custom in the future", url.Values{"from": {"2026-10-28T10:00"}, "to": {"2026-10-29T10:00"}}, "2026-10-26T12:00:00Z", "2026-10-27T12:00:00Z", "24h", true},
		{"custom too long", url.Values{"from": {"2025-01-01T00:00"}, "to": {"2026-10-01T00:00"}}, "2026-10-26T12:00:00Z", "2026-10-27T12:00:00Z", "24h", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Parse(c.q, now, bel)
			if got := r.From.UTC().Format(time.RFC3339); got != c.from {
				t.Errorf("from %s, want %s", got, c.from)
			}
			if got := r.To.UTC().Format(time.RFC3339); got != c.to {
				t.Errorf("to %s, want %s", got, c.to)
			}
			if r.Preset != c.preset || (r.Error != "") != c.err {
				t.Errorf("preset %q error %q", r.Preset, r.Error)
			}
		})
	}
}
