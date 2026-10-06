package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/engine"
	"github.com/drilonrecica/sinjal/internal/notify"
	"github.com/drilonrecica/sinjal/internal/store"
)

// The whole chain: a real check against a target that fails, the result
// processor's intent, the engine's callback, routing, notify.Send, and a
// webhook endpoint that receives the payload of docs/36; then the recovery.
func TestEndToEndThroughTheEngine(t *testing.T) {
	e := newEnv(t)
	var status atomic.Int32
	status.Store(http.StatusInternalServerError)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	defer target.Close()

	var mu sync.Mutex
	var payloads []map[string]any
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p map[string]any
		_ = json.Unmarshal(b, &p)
		mu.Lock()
		payloads = append(payloads, p)
		mu.Unlock()
	}))
	defer hook.Close()
	received := func(n int) func() bool {
		return func() bool { mu.Lock(); defer mu.Unlock(); return len(payloads) >= n }
	}

	p := e.profile("p", false, "", "", true)
	ch, err := store.CreateChannel(context.Background(), e.d, e.key, store.ChannelInput{Name: "Hook", Enabled: true,
		Config: notify.Webhook{URL: hook.URL + "/sinjal"}}, base)
	if err != nil {
		t.Fatal(err)
	}
	e.route(p, ch, "info", "critical")
	m, err := store.CreateMonitor(context.Background(), e.d, store.MonitorInput{Name: "API", Enabled: true, RetryDelayMS: 20,
		TimeoutMS: 5000, NotificationProfileID: p, HTTP: store.HTTPConfig{URL: target.URL, FollowRedirects: true}}, base)
	if err != nil {
		t.Fatal(err)
	}

	disp := New(e.d, e.key, time.UTC, nil, nil, e.log)
	dctx, stopDispatch := context.WithCancel(context.Background())
	dispatched := make(chan struct{})
	go func() { disp.Run(dctx); close(dispatched) }()
	eng := engine.New(e.d, e.key, 2, "Sinjal/test", time.UTC, nil, nil, disp.Enqueue, e.log)
	ctx, cancel := context.WithCancel(context.Background())
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); eng.Wait(); stopDispatch(); <-dispatched })

	e.wait("the DOWN payload", received(1))
	mu.Lock()
	first := payloads[0]
	mu.Unlock()
	if first["event"] != "monitor.down" || first["severity"] != "critical" || first["monitor"].(map[string]any)["name"] != "API" ||
		first["incident"] == nil || first["attempts"] != 2.0 {
		t.Errorf("DOWN payload = %v", first)
	}
	e.wait("the DOWN record", func() bool {
		return e.count(`SELECT COUNT(*) FROM notification_deliveries WHERE status = 'sent' AND event_type = 'down'`) == 1
	})

	status.Store(http.StatusOK)
	if err := eng.Schedule(context.Background(), m); err != nil { // a prompt check, like an edit
		t.Fatal(err)
	}
	e.wait("the RECOVERY payload", received(2))
	mu.Lock()
	second := payloads[1]
	mu.Unlock()
	if second["event"] != "monitor.recovered" || second["severity"] != "info" || second["incident"].(map[string]any)["duration_seconds"] == nil {
		t.Errorf("RECOVERY payload = %v", second)
	}
	if n := e.count(`SELECT COUNT(*) FROM incidents WHERE monitor_id = ? AND down_notified_at IS NOT NULL AND recovery_notified_at IS NOT NULL`, m); n != 1 {
		t.Error("the incident's notification state is not set")
	}
	if c := e.health(ch); c.HealthState != store.HealthHealthy {
		t.Errorf("health = %+v", c)
	}
}
