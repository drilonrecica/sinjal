package engine

import (
	"context"
	"errors"
	"time"

	"github.com/drilonrecica/sinjal/internal/monitor/dnscheck"
	"github.com/drilonrecica/sinjal/internal/monitor/icmpcheck"
	"github.com/drilonrecica/sinjal/internal/monitor/tcpcheck"
	"github.com/drilonrecica/sinjal/internal/results"
	"github.com/drilonrecica/sinjal/internal/store"
)

// runOther executes the check of a monitor that is not HTTP. Each type
// reads its own config row and calls its own check; only the result shape
// is shared.
func (e *Engine) runOther(ctx context.Context, m store.Monitor, started time.Time) (results.Result, bool) {
	timeout := time.Duration(m.TimeoutMS) * time.Millisecond
	var res results.Result
	var err error
	switch m.Type {
	case store.TypeHeartbeat:
		return e.heartbeat(ctx, m, started)
	case store.TypeTCP:
		var c store.TCPConfig
		if c, err = store.GetTCPConfig(ctx, e.db.Reader, m.ID); err == nil {
			r := tcpcheck.Check(ctx, tcpcheck.Config{Host: c.Host, Port: c.Port, Timeout: timeout})
			res = baseResult(m.ID, r.Started, r.Duration, r.Success, r.Kind, r.Message)
		}
	case store.TypeICMP:
		var c store.ICMPConfig
		if c, err = store.GetICMPConfig(ctx, e.db.Reader, m.ID); err == nil {
			r := e.icmp.Check(ctx, icmpcheck.Config{Host: c.Host, Timeout: timeout})
			res = baseResult(m.ID, r.Started, r.Duration, r.Success, r.Kind, r.Message)
		}
	case store.TypeDNS:
		var c store.DNSConfig
		if c, err = store.GetDNSConfig(ctx, e.db.Reader, m.ID); err == nil {
			r := dnscheck.Check(ctx, dnscheck.Config{
				Hostname: c.Hostname, QueryType: c.QueryType, Resolver: c.Resolver,
				Expected: c.Expected, MatchMode: c.MatchMode, Timeout: timeout,
			})
			res = baseResult(m.ID, r.Started, r.Duration, r.Success, r.Kind, r.Message)
			res.Snippet = r.Snippet
		}
	default:
		return unusable(m.ID, started, "unknown monitor type "+m.Type), true
	}
	if errors.Is(err, store.ErrNotFound) {
		return results.Result{}, false // deleted while the job was waiting
	}
	if err != nil {
		e.readFailed(ctx, m.ID, err)
		return results.Result{}, false
	}
	return res, true
}

func baseResult(id string, started time.Time, d time.Duration, ok bool, kind, msg string) results.Result {
	return results.Result{MonitorID: id, CheckedAt: started, Duration: d, Success: ok, Kind: kind, Message: msg}
}
