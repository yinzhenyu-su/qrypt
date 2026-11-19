package upload

import (
	"context"
	"errors"
)

// ErrServiceClosed reports that the service stopped while upload work was
// still outstanding. WaitIdle returns it instead of blocking forever on a
// service that will never make progress again.
var ErrServiceClosed = errors.New("upload: service closed with pending work")

// Idle reports whether the upload engine has drained: nothing is queued,
// scheduled, claimed or in flight, and the journal has no pending record left.
//
// It is the observation half of WaitIdle and is intended for tests and
// diagnostics; production code has no reason to poll it.
func (s *Service) Idle() bool {
	if s == nil {
		return true
	}
	s.scheduledMu.Lock()
	schedulingIdle := len(s.scheduled) == 0 && len(s.deadlines) == 0 && len(s.claimed) == 0 && len(s.queue) == 0
	closed := s.closed
	s.scheduledMu.Unlock()
	if closed {
		// A closed service makes no further progress, so only the durable
		// record matters for the answer.
		return len(s.store.PendingUploads()) == 0
	}
	if !schedulingIdle {
		return false
	}
	if len(s.store.PendingUploads()) != 0 {
		return false
	}
	return len(s.debug.ActiveSnapshots()) == 0
}

// WaitIdle blocks until the upload engine is idle or ctx is done. It replaces
// bounded polling loops in tests: every state transition that can change the
// answer broadcasts on the worker wake channel, so no polling interval and no
// lost wakeup is possible.
func (s *Service) WaitIdle(ctx context.Context) error {
	if s == nil {
		return nil
	}
	for {
		s.scheduledMu.Lock()
		wake := s.wake
		done := s.done
		closed := s.closed
		s.scheduledMu.Unlock()
		if s.Idle() {
			return nil
		}
		if closed {
			return ErrServiceClosed
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			if s.Idle() {
				return nil
			}
			return ErrServiceClosed
		case <-wake:
		}
	}
}
