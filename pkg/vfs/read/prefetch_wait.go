package read

import (
	"context"
	"time"
)

// waitTarget joins validated block delivery. Slow, useful progress inside the
// target renews the wait, but unrelated prefix/tail traffic and replay do not.
// The executor's original total deadline and caller cancellation remain bounds.
func (w *windowLoad) waitTarget(ctx context.Context, index int64) (ready bool, err error) {
	var timer *time.Timer
	var timeout <-chan time.Time
	if w.prefetch {
		timer = time.NewTimer(prefetchWaitBudget)
		defer timer.Stop()
		timeout = timer.C
	}
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-w.readyFor(index):
			return true, nil
		case <-timeout:
			// Prefer bytes already published when readiness races the timer.
			select {
			case <-w.readyFor(index):
				return true, nil
			default:
			}
			end, advanced := w.trace.BodyProgress()
			if w.progressive && w.streams == 1 &&
				end > index*ChunkSize && end <= (index+1)*ChunkSize {
				if remaining := time.Until(advanced.Add(prefetchWaitBudget)); remaining > 0 {
					timer.Reset(remaining)
					continue
				}
			}
			return false, nil
		}
	}
}
