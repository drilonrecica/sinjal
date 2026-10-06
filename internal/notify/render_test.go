package notify

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/incident"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

var (
	cest    = time.FixedZone("CEST", 2*3600)
	failed  = time.Date(2026, 10, 6, 16, 42, 13, 0, time.UTC) // 18:42:13 CEST
	started = time.Date(2026, 10, 6, 16, 40, 0, 0, time.UTC)
)

func ms(n int) *time.Duration { d := time.Duration(n) * time.Millisecond; return &d }

func baseEvent(k Kind) Event {
	return Event{Kind: k, MonitorID: "m1", MonitorName: "API", MonitorType: "http", IncidentID: "i1", IncidentStart: started, At: failed}
}

func goldenEvents() map[string]Event {
	down := baseEvent(KindDown)
	down.Reason, down.Attempts, down.Latency = "timeout after 5s", 2, ms(74)

	downTest := down
	downTest.Test = true

	downStatus := down
	downStatus.StatusURL = "https://status.example.com/"

	downMinimal := baseEvent(KindDown) // no reason, attempts, latency or time
	downMinimal.At = time.Time{}
	downMinimal.IncidentID = ""

	hostile := down
	hostile.MonitorName = "API\r\n@everyone <b>x</b>"
	hostile.Reason = "dial tcp 10.0.0.1:443:\n\tconnection refused\x07 @here " + strings.Repeat("long ", 80)

	recovery := baseEvent(KindRecovery)
	recovery.Duration, recovery.Latency = 4*time.Minute+17*time.Second, ms(51)

	recoveryNoLatency := baseEvent(KindRecovery)
	recoveryNoLatency.Duration = 3*time.Hour + 2*time.Minute

	tls := baseEvent(KindTLSWarning)
	tls.MonitorName, tls.IncidentID, tls.IncidentStart = "Website", "", time.Time{}
	tls.CertExpiry, tls.DaysLeft = time.Date(2026, 10, 20, 22, 30, 0, 0, time.UTC), 14

	reminder := baseEvent(KindReminder)
	reminder.Duration, reminder.Reason = time.Hour+2*time.Minute, "connection refused"

	flapping := baseEvent(KindFlapping)
	stable := baseEvent(KindStable)
	stable.IncidentID, stable.IncidentStart = "", time.Time{}

	recoveryTest := recovery
	recoveryTest.Test = true

	return map[string]Event{
		"down": down, "down_test": downTest, "down_status_url": downStatus, "down_minimal": downMinimal, "down_hostile": hostile,
		"recovery": recovery, "recovery_no_latency": recoveryNoLatency, "recovery_test": recoveryTest,
		"tls_warning": tls, "reminder": reminder, "flapping": flapping, "stable": stable,
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

func TestGoldenMessages(t *testing.T) {
	for name, e := range goldenEvents() {
		t.Run(name, func(t *testing.T) {
			m := Render(e, cest)
			golden(t, name+".text", []byte(m.Text()+"\n"))
			golden(t, name+".telegram", []byte(m.Telegram()+"\n"))
			subject, body := m.Email()
			golden(t, name+".email", []byte("Subject: "+subject+"\n\n"+body))
			dc, err := m.Discord()
			if err != nil {
				t.Fatal(err)
			}
			golden(t, name+".discord", pretty(t, dc))
			wh, err := m.Webhook()
			if err != nil {
				t.Fatal(err)
			}
			golden(t, name+".webhook", append(wh, '\n'))
		})
	}
}

func pretty(t *testing.T, raw []byte) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	return append(out, '\n')
}

// The texts of docs/36, written out in full: the golden files hold the
// rest.
func TestTextsMatchTheSpecExamples(t *testing.T) {
	ev := goldenEvents()
	utc := time.UTC
	cases := []struct {
		name string
		want string
	}{
		{"recovery", "API recovered\nDowntime: 4m 17s\nCurrent latency: 51 ms"},
		{"tls_warning", "Website TLS certificate expires in 14 days\nExpiry: 2026-10-20"},
		{"flapping", "API is flapping\nRepeated state changes detected in the last 10 minutes.\nFurther transition notifications are temporarily suppressed."},
		{"stable", "API is stable again\nCurrently UP. No state changes in the last 10 minutes."},
		{"reminder", "API is still DOWN\nDuration: 1h 02m\nReason: connection refused"},
		{"down", "API is DOWN\nReason: timeout after 5s\nFailed at: 2026-10-06 16:42:13 UTC\nAttempts: 2\nLast latency: 74 ms"},
		{"down_test", "[TEST] API is DOWN\nThis is a simulated Sinjal incident.\nReason: timeout after 5s\nFailed at: 2026-10-06 16:42:13 UTC\nAttempts: 2\nLast latency: 74 ms"},
	}
	for _, tc := range cases {
		if got := Render(ev[tc.name], utc).Text(); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestTimestampsUseTheInstanceZone(t *testing.T) {
	e := goldenEvents()["down"]
	if got := Render(e, cest).Text(); !strings.Contains(got, "Failed at: 2026-10-06 18:42:13 CEST") {
		t.Errorf("CEST: %s", got)
	}
	if got := Render(e, nil).Text(); !strings.Contains(got, "Failed at: 2026-10-06 16:42:13 UTC") {
		t.Errorf("nil zone must be UTC: %s", got)
	}
	// The expiry date is the date in the instance zone: 22:30 UTC is the next day in CEST.
	if got := Render(goldenEvents()["tls_warning"], cest).Text(); !strings.Contains(got, "Expiry: 2026-10-21") {
		t.Errorf("expiry date: %s", got)
	}
}

func TestFormatDuration(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{-time.Minute, "0s"}, {0, "0s"}, {400 * time.Millisecond, "0s"}, {600 * time.Millisecond, "1s"},
		{45 * time.Second, "45s"}, {59*time.Second + 600*time.Millisecond, "1m 00s"}, {time.Minute, "1m 00s"},
		{4*time.Minute + 17*time.Second, "4m 17s"}, {59*time.Minute + 59*time.Second, "59m 59s"},
		{time.Hour, "1h 00m"}, {time.Hour + 2*time.Minute + 30*time.Second, "1h 02m"}, {23*time.Hour + 59*time.Minute, "23h 59m"},
		{24 * time.Hour, "1d 00h"}, {50*time.Hour + 30*time.Minute, "2d 02h"},
	} {
		if got := FormatDuration(tc.d); got != tc.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestFormatLatency(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "<1 ms"}, {999 * time.Microsecond, "<1 ms"}, {time.Millisecond, "1 ms"}, {74 * time.Millisecond, "74 ms"},
		{999 * time.Millisecond, "999 ms"}, {time.Second, "1.0 s"}, {1540 * time.Millisecond, "1.5 s"},
	} {
		if got := FormatLatency(tc.d); got != tc.want {
			t.Errorf("FormatLatency(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestExpiryWording(t *testing.T) {
	for days, want := range map[int]string{-3: "has expired", 0: "expires today", 1: "expires in 1 day", 2: "expires in 2 days"} {
		if got := expiryText(days); got != want {
			t.Errorf("expiryText(%d) = %q, want %q", days, got, want)
		}
	}
}

func TestSeverityAndKind(t *testing.T) {
	want := map[Kind]Severity{KindDown: SeverityCritical, KindReminder: SeverityCritical, KindRecovery: SeverityInfo,
		KindStable: SeverityInfo, KindTLSWarning: SeverityWarning, KindFlapping: SeverityWarning}
	for k, s := range want {
		if got := SeverityOf(k); got != s {
			t.Errorf("SeverityOf(%s) = %s, want %s", k, got, s)
		}
		if got := Render(Event{Kind: k, MonitorName: "x"}, nil).Severity; got != s {
			t.Errorf("Render(%s).Severity = %s, want %s", k, got, s)
		}
	}
	for in, out := range map[incident.IntentKind]Kind{incident.IntentDown: KindDown, incident.IntentRecovery: KindRecovery,
		incident.IntentFlapping: KindFlapping, incident.IntentStable: KindStable, incident.IntentTLSWarning: KindTLSWarning,
		incident.IntentReminder: KindReminder} {
		if got := KindOf(in); got != out {
			t.Errorf("KindOf(%s) = %s, want %s", in, got, out)
		}
	}
}

func TestOneLineMakesValuesSafe(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain", "plain"}, {"  a \t b\r\nc\x00d ", "a b c d"}, {"", ""}, {"\n\n", ""},
	} {
		if got := oneLine(tc.in, 50); got != tc.want {
			t.Errorf("oneLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	got := oneLine(strings.Repeat("é", 300), 10)
	if r := []rune(got); len(r) != 10 || r[9] != '…' {
		t.Errorf("a long value must be cut at 10 characters ending in an ellipsis, got %q", got)
	}
}

func TestHostileTextStaysOnItsLineAndFitsLimits(t *testing.T) {
	m := Render(goldenEvents()["down_hostile"], nil)
	if strings.ContainsAny(m.Title, "\r\n\a") {
		t.Errorf("title has a line break: %q", m.Title)
	}
	for _, l := range m.Lines {
		if strings.ContainsAny(l, "\r\n\a") {
			t.Errorf("a line holds a line break: %q", l)
		}
		if len([]rune(l)) > maxReasonRunes+len("Reason: ") {
			t.Errorf("a line is %d characters", len([]rune(l)))
		}
	}
	subject, _ := m.Email()
	if strings.ContainsAny(subject, "\r\n") {
		t.Errorf("the subject could inject headers: %q", subject)
	}
}

func TestDiscordIsSafeAndBounded(t *testing.T) {
	m := Render(goldenEvents()["down_hostile"], nil)
	raw, err := m.Discord()
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Embeds []struct {
			Title, Description string
			Color              int
			Timestamp          string
		}
		AllowedMentions struct{ Parse []string } `json:"allowed_mentions"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Embeds) != 1 || p.AllowedMentions.Parse == nil || len(p.AllowedMentions.Parse) != 0 {
		t.Errorf("payload = %s", raw)
	}
	if !strings.Contains(string(raw), `"parse":[]`) {
		t.Errorf("allowed_mentions.parse must be an empty list, not null: %s", raw)
	}

	long := Message{Title: strings.Repeat("t", 1000), Lines: []string{strings.Repeat("d", 9000)}}
	raw, _ = long.Discord()
	json.Unmarshal(raw, &p)
	if n := len([]rune(p.Embeds[0].Title)); n != discordTitle {
		t.Errorf("title = %d characters, want %d", n, discordTitle)
	}
	if n := len([]rune(p.Embeds[0].Description)); n != discordBody {
		t.Errorf("description = %d characters, want %d", n, discordBody)
	}
}

func TestDiscordColors(t *testing.T) {
	colors := map[string]int{}
	for name, e := range goldenEvents() {
		raw, _ := Render(e, nil).Discord()
		var p struct{ Embeds []struct{ Color int } }
		json.Unmarshal(raw, &p)
		colors[name] = p.Embeds[0].Color
	}
	if colors["down"] != 0xE5484D || colors["recovery"] != 0x30A46C || colors["flapping"] != 0xF5A524 || colors["down_test"] != 0x8B8D98 || colors["recovery_test"] != 0x8B8D98 {
		t.Errorf("colors = %x", colors)
	}
}

func TestWebhookPayload(t *testing.T) {
	decode := func(name string) map[string]any {
		raw, err := Render(goldenEvents()[name], nil).Webhook()
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return v
	}
	down := decode("down")
	if down["event"] != "monitor.down" || down["severity"] != "critical" || down["reason"] != "timeout after 5s" || down["test"] != nil {
		t.Errorf("down = %v", down)
	}
	mon := down["monitor"].(map[string]any)
	if mon["id"] != "m1" || mon["name"] != "API" || mon["type"] != "http" {
		t.Errorf("monitor = %v", mon)
	}
	inc := down["incident"].(map[string]any)
	if inc["id"] != "i1" || inc["started_at"] != "2026-10-06T16:40:00Z" || inc["duration_seconds"] != nil {
		t.Errorf("down incident = %v", inc)
	}
	if down["latency_ms"] != float64(74) || down["attempts"] != float64(2) || down["timestamp"] != "2026-10-06T16:42:13Z" {
		t.Errorf("down = %v", down)
	}

	rec := decode("recovery")
	if rec["event"] != "monitor.recovered" || rec["severity"] != "info" || rec["incident"].(map[string]any)["duration_seconds"] != float64(257) {
		t.Errorf("recovery = %v", rec)
	}
	if got := decode("reminder")["incident"].(map[string]any)["duration_seconds"]; got != float64(3720) {
		t.Errorf("reminder duration = %v", got)
	}
	tls := decode("tls_warning")
	if tls["incident"] != nil || tls["event"] != "monitor.tls_warning" || tls["certificate"].(map[string]any)["days_remaining"] != float64(14) {
		t.Errorf("tls = %v", tls)
	}
	if decode("down_test")["test"] != true {
		t.Error("a simulated alert must say test: true")
	}
	if decode("down_status_url")["status_page_url"] != "https://status.example.com/" {
		t.Error("status_page_url missing")
	}
	events := map[string]string{"flapping": "monitor.flapping", "stable": "monitor.stable", "reminder": "monitor.reminder"}
	for name, want := range events {
		if got := decode(name)["event"]; got != want {
			t.Errorf("%s event = %v, want %s", name, got, want)
		}
	}
}

func TestTestMarkerIsUnmistakableOnEveryChannel(t *testing.T) {
	m := Render(goldenEvents()["recovery_test"], nil)
	subject, body := m.Email()
	dc, _ := m.Discord()
	wh, _ := m.Webhook()
	for name, got := range map[string]string{"text": m.Text(), "telegram": m.Telegram(), "email subject": subject, "email body": body, "discord": string(dc)} {
		if !strings.Contains(got, "[TEST]") || !strings.Contains(got, "simulated") && name != "email subject" {
			t.Errorf("%s does not mark the message as a test: %q", name, got)
		}
	}
	if !strings.Contains(string(wh), `"test": true`) {
		t.Errorf("webhook does not mark the message as a test: %s", wh)
	}
}

func TestNoMessageCarriesAnythingButTheSafeVariables(t *testing.T) {
	// A message is built from Event alone: whatever a monitor's configuration
	// held (a URL with a token, a header) has no way in.
	e := goldenEvents()["down"]
	for _, got := range []string{Render(e, nil).Text(), string(mustWebhook(t, e))} {
		for _, leak := range []string{"Authorization", "Bearer", "password", "token="} {
			if strings.Contains(got, leak) {
				t.Errorf("message contains %q: %s", leak, got)
			}
		}
	}
}

func mustWebhook(t *testing.T, e Event) []byte {
	t.Helper()
	b, err := Render(e, nil).Webhook()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
