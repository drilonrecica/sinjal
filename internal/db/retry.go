package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
)

// sqliteBusy is the primary result code SQLITE_BUSY; extended codes keep it
// in the low byte.
const sqliteBusy = 5

// retryDelays is the backoff between attempts (docs/09_DATABASE.md). A var so
// tests can shrink it.
var retryDelays = []time.Duration{
	25 * time.Millisecond,
	100 * time.Millisecond,
	250 * time.Millisecond,
	time.Second,
}

// BusyExhaustedError is returned when SQLITE_BUSY persisted through every
// retry. Callers must surface it (system warning) rather than drop the write.
type BusyExhaustedError struct {
	Attempts int
	Err      error
}

func (e *BusyExhaustedError) Error() string {
	return fmt.Sprintf("database busy after %d attempts: %v", e.Attempts, e.Err)
}

func (e *BusyExhaustedError) Unwrap() error { return e.Err }

// IsBusy reports whether err is a SQLITE_BUSY failure.
func IsBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqliteBusy
}

// Retry runs fn, retrying with bounded backoff while it fails with
// SQLITE_BUSY. fn must be safe to call again (run a whole transaction inside
// it, not half of one). Other errors are returned immediately.
func Retry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = fn(); err == nil || !IsBusy(err) {
			return err
		}
		if attempt == len(retryDelays) {
			return &BusyExhaustedError{Attempts: attempt + 1, Err: err}
		}
		t := time.NewTimer(retryDelays[attempt])
		select {
		case <-ctx.Done():
			t.Stop()
			return errors.Join(ctx.Err(), err)
		case <-t.C:
		}
	}
}
