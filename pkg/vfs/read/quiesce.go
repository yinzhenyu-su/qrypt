package read

import "context"

// Quiesce waits until the reader has no work a caller must observe: no window
// load (foreground or speculative), no speculative scope job and no
// foreground Read call. Tests use it instead of sleeping after a read that
// starts lookahead work; a wait returns as soon as the last waiter-visible
// operation finishes.
//
// ctx bounds the wait. A read that is itself blocked on the caller (for
// example a body nobody drains) keeps Quiesce blocked, so callers must only
// quiesce work they have already released.
func (r *Reader) Quiesce(ctx context.Context) error {
	if r == nil {
		return nil
	}
	for {
		if r.state.quiescent() && r.inflight.Load() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.state.quiesceChanged():
		}
	}
}

// quiescent reports whether the read domain has no window load and no
// speculative job in flight.
func (s *State) quiescent() bool {
	if s == nil {
		return true
	}
	return s.windowsEmpty() && s.scopes.idle()
}
