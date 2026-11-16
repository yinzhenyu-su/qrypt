package readbudget

import (
	"context"
	"fmt"
	"sync/atomic"
)

var testSlotSequence atomic.Uint64

func acquireTestSlot(s *Scheduler, ctx context.Context, p Priority) (func(), error) {
	return s.AcquireRange(ctx, fmt.Sprintf("test-slot-%d", testSlotSequence.Add(1)), 32, p)
}
