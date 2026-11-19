package read

import "context"

// waitTarget joins validated block delivery: it waits until the block is
// published, the fetch finishes without it, or the caller is canceled. The
// fetch's own idle/total deadlines bound a stalled transfer, so there is no
// separate foreground budget that abandons and re-requests in-flight work.
func (w *windowLoad) waitTarget(ctx context.Context, index int64) (ready bool, err error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-w.readyFor(index):
		return true, nil
	}
}
