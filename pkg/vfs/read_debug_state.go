package vfs

import (
	"sync"
	"sync/atomic"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// readHistoryState is the bounded read-event ring for debug snapshots.
// Owned by the read domain (readState.history); mu guards events/pos/
// count/sequence. Lifecycle: created in newReadState, bounded by
// debugReadHistoryLimit, reset by debug commands.
type readHistoryState struct {
	mu       sync.Mutex
	events   []drive.MetricEvent // ring buffer; lazily grown toward debugReadHistoryLimit
	pos      int                 // ring index of the next write slot
	count    int                 // number of live events (<= cap(events))
	sequence uint64
}

func newReadHistoryState() *readHistoryState {
	return &readHistoryState{}
}

// debugActiveSlots is the fixed capacity of the active-debug ring. Active
// operations are short-lived (microseconds), so 128 concurrent ops is a
// generous bound; when full, Begin returns 0 (tracking skipped).
const debugActiveSlots = 128

type debugActiveSlot struct {
	seq atomic.Uint64 // 0 = empty, otherwise the operation sequence occupying the slot
	mu  sync.RWMutex
	op  DebugActiveOp
}

type activeDebugState struct {
	sequence atomic.Uint64
	slots    [debugActiveSlots]debugActiveSlot
}

func newActiveDebugState() *activeDebugState {
	return &activeDebugState{}
}
