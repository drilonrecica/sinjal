package store

import (
	"errors"
	"testing"
)

func TestCheckMonitor(t *testing.T) {
	d := testDB(t)
	ctx := t.Context()
	if err := CheckMonitor(ctx, d.Reader, "", MonitorInput{Name: "a", HTTP: HTTPConfig{URL: "https://example.com"}}); err != nil {
		t.Fatalf("valid monitor: %v", err)
	}
	err := CheckMonitor(ctx, d.Reader, "", MonitorInput{ParentMonitorID: "missing", HTTP: HTTPConfig{URL: "ftp://x"}})
	var fe FieldErrors
	if !errors.As(err, &fe) || fe["name"] == "" || fe["url"] == "" || fe["parent_monitor_id"] == "" {
		t.Fatalf("got %v, want name, url and parent errors", err)
	}
	var n int
	if err := d.Reader.QueryRow(`SELECT COUNT(*) FROM monitors`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a check wrote %d monitors (%v)", n, err)
	}
}
