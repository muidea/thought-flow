package documentsetflow

import (
	"context"
	"time"

	"thoughtflow/internal/pkg/ai"
	ds "thoughtflow/internal/pkg/documentset"
)

// Only explicit gateway evidence that execution never started permits an
// automatic retry. Unknown completions require a separate acknowledged attempt.
func boundedCall[T any](ctx context.Context, s *Service, run *ds.Run, kind, key string, input any, call func(context.Context) (T, error)) (T, error) {
	var value T
	for attempt := 0; attempt < 3; attempt++ {
		if e := ctx.Err(); e != nil {
			return value, e
		}
		index, e := s.callStart(run, kind, key, input)
		if e != nil {
			return value, e
		}
		var err error
		value, err = call(ctx)
		if e = s.callDone(run, index, value, err); e == nil {
			return value, nil
		}
		if err == nil || !ai.CanAutomaticallyRetry(e) || attempt == 2 {
			return value, e
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * 250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return value, ctx.Err()
		case <-timer.C:
		}
	}
	return value, ds.ErrUnavailable
}
