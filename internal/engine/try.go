package engine

import (
	"context"
	"errors"
	"time"

	"github.com/drilonrecica/sinjal/internal/results"
	"github.com/drilonrecica/sinjal/internal/secret"
	"github.com/drilonrecica/sinjal/internal/store"
)

// Try runs one check of a monitor that is not saved, for the form's "Test
// now" (docs/03). in should be valid (store.PrepareMonitor); secrets are
// the decrypted values its HTTP check needs. Nothing is stored, scheduled
// or announced. The certificate expiry is reported whenever one was seen.
// A heartbeat monitor runs no check, so it cannot be tried.
func (e *Engine) Try(ctx context.Context, in store.MonitorInput, secrets map[string]secret.String) (results.Result, error) {
	in = in.WithDefaults()
	timeout := time.Duration(in.TimeoutMS) * time.Millisecond
	switch in.Type {
	case store.TypeHeartbeat:
		return results.Result{}, errors.New("a heartbeat monitor runs no check of its own")
	case store.TypeTCP, store.TypeICMP, store.TypeDNS:
		return e.otherCheck(ctx, "", in, timeout), nil
	case store.TypeHTTP:
		cfg, err := buildHTTPConfig(store.Monitor{TimeoutMS: in.TimeoutMS}, in.HTTP, secrets)
		if err != nil {
			return results.Result{}, err
		}
		return toResult("", true, e.http.Check(ctx, cfg)), nil
	}
	return results.Result{}, errors.New("unknown monitor type " + in.Type)
}
