// Package observe owns the runtime observation state for the VFS debug
// layer: in-flight operation tracking in a lock-free ring buffer. It is a
// pure state store - no Debug* query API and no dependency on pkg/vfs.
package observe

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// ActiveSlots is the ring-buffer size for in-flight operation tracking.
// Active ops are short-lived, so a full ring is rare; when every slot is
// busy the new op is skipped (transient, no data loss for existing ops).
const ActiveSlots = 128

type activeSlot struct {
	seq atomic.Uint64 // 0 = empty, otherwise the op sequence in the slot
	mu  sync.RWMutex
	op  vfstypes.DebugActiveOp
}

// ActiveStore tracks in-flight operations. Begin/Update/Finish/Snapshot;
// the store owns no other state. Not safe for concurrent Close with Begin.
type ActiveStore struct {
	sequence atomic.Uint64
	slots    [ActiveSlots]activeSlot
	mount    string // default mount name stamped on ops
}

// NewActiveStore creates an empty store that stamps unset mount names with
// the given mount.
func NewActiveStore(mount string) *ActiveStore {
	return &ActiveStore{mount: mount}
}

// Begin registers an op and returns its ID (0 when the ring is full).
func (s *ActiveStore) Begin(op vfstypes.DebugActiveOp) uint64 {
	if op.OpID == "" {
		op.OpID = fmt.Sprintf("active-%d", s.sequence.Add(1))
	}
	if op.Mount == "" {
		op.Mount = s.mount
	}
	if op.State == "" {
		op.State = "active"
	}
	now := util.Now()
	op.StartedAt = now
	op.UpdatedAt = now
	// Linear probe for a free slot; an empty slot is almost always found
	// on the first try, no shared mutex.
	for i := 0; i < ActiveSlots; i++ {
		seq := s.sequence.Add(1)
		slot := &s.slots[seq%ActiveSlots]
		slot.mu.Lock()
		if slot.seq.Load() == 0 {
			slot.op = op
			slot.seq.Store(seq)
			slot.mu.Unlock()
			return seq
		}
		slot.mu.Unlock()
	}
	return 0
}

// Update applies fn to the op with the given ID (no-op when unknown).
func (s *ActiveStore) Update(opID uint64, fn func(*vfstypes.DebugActiveOp)) {
	if opID == 0 {
		return
	}
	slot := &s.slots[opID%ActiveSlots]
	if slot.seq.Load() != opID {
		return
	}
	slot.mu.Lock()
	if slot.seq.Load() == opID {
		fn(&slot.op)
		slot.op.UpdatedAt = util.Now()
	}
	slot.mu.Unlock()
}

// Finish removes the op with the given ID (no-op when unknown).
func (s *ActiveStore) Finish(opID uint64) {
	if opID == 0 {
		return
	}
	slot := &s.slots[opID%ActiveSlots]
	slot.mu.Lock()
	if slot.seq.Load() == opID {
		slot.op = vfstypes.DebugActiveOp{}
		slot.seq.Store(0)
	}
	slot.mu.Unlock()
}

// Snapshot returns deep copies of all in-flight ops, oldest first by
// started-at time (stable sort by OpID for equal timestamps).
func (s *ActiveStore) Snapshot() []vfstypes.DebugActiveOp {
	now := util.Now()
	ops := make([]vfstypes.DebugActiveOp, 0, ActiveSlots)
	for i := range s.slots {
		slot := &s.slots[i]
		if slot.seq.Load() == 0 {
			continue
		}
		slot.mu.RLock()
		item := CloneOp(slot.op)
		slot.mu.RUnlock()
		item.AgeMS = durationMillis(now.Sub(item.StartedAt))
		ops = append(ops, item)
	}
	return ops
}

// CloneOp deep-copies an op so snapshots never share mutable Extra maps.
func CloneOp(op vfstypes.DebugActiveOp) vfstypes.DebugActiveOp {
	if op.Extra == nil {
		return op
	}
	extra := make(map[string]any, len(op.Extra))
	for k, v := range op.Extra {
		extra[k] = v
	}
	op.Extra = extra
	return op
}

func durationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Millisecond - 1) / time.Millisecond)
}
