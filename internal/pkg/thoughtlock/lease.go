package thoughtlock

import (
	"context"
	"errors"
	"strings"
	"time"
)

func IsWorkflowSession(id string) bool {
	for _, kind := range []string{RefinerSessionID, ExpanderSessionID, IndexerSessionID} {
		if id == kind || strings.HasPrefix(id, kind+":") {
			return true
		}
	}
	return false
}

// Lease waits without claiming success for a busy resource. The token is unique
// to the job; its heartbeat and release cannot extend or release a sibling job.
func (l *Locker) Lease(ctx context.Context, id, token string) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := l.Acquire(id, token)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrLocked) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		interval := l.ttl / 3
		if interval <= 0 {
			interval = time.Nanosecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = l.Heartbeat(id, token)
			}
		}
	}()
	return func() { close(stop); <-done; l.Release(id, token) }, nil
}
