package vfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type readPrefetchContextKey struct{}
type dirPrefetchContextKey struct{}

func WithoutReadPrefetch(ctx context.Context) context.Context {
	return context.WithValue(ctx, readPrefetchContextKey{}, true)
}

func readPrefetchEnabled(ctx context.Context) bool {
	disabled, _ := ctx.Value(readPrefetchContextKey{}).(bool)
	return !disabled
}

func WithoutDirPrefetch(ctx context.Context) context.Context {
	return context.WithValue(ctx, dirPrefetchContextKey{}, true)
}

func dirPrefetchEnabled(ctx context.Context) bool {
	disabled, _ := ctx.Value(dirPrefetchContextKey{}).(bool)
	return !disabled
}

// ReadPriority controls how read slots are allocated under contention.
type ReadPriority int

const (
	PriorityLow    ReadPriority = iota // prefetch, anticipatory reads
	PriorityNormal                     // default user-initiated reads
	PriorityHigh                       // UI-critical reads (thumbnails, visible content)
)

type readPriorityKey struct{}

func WithReadPriority(ctx context.Context, p ReadPriority) context.Context {
	return context.WithValue(ctx, readPriorityKey{}, p)
}

func readPriority(ctx context.Context) ReadPriority {
	p, _ := ctx.Value(readPriorityKey{}).(ReadPriority)
	return p
}

// --- read_debug_helpers.go ---

func readWindowExtra(windowChunks int) map[string]any {
	return map[string]any{
		"window_chunks": windowChunks,
	}
}
func readCacheLookupExtra(source string, started time.Time, extra map[string]any) map[string]any {
	out := cloneReadExtra(extra)
	if out == nil {
		out = map[string]any{}
	}
	out["cache_lookup_source"] = source
	out["cache_lookup_ms"] = durationMillis(timeutil.Now().Sub(started))
	return out
}
func (v *VFS) recordReadChunkDetail(ctx context.Context, entry drive.Entry, phase string, index, start, size, bytes int64, started time.Time, extra map[string]any, err error) {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok || op.OpID == "" {
		return
	}
	if extra == nil {
		extra = map[string]any{}
	}
	extra["chunk_index"] = index
	extra["chunk_offset"] = index * readChunkSize
	extra["chunk_range_start"] = start
	extra["chunk_range_size"] = size
	v.recordDebugReadDetail(ctx, op.Name, entry.ID, phase, index*readChunkSize+start, size, bytes, started, extra, err)
}
func debugOperationName(ctx context.Context) string {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok {
		return ""
	}
	return op.Name
}
func cloneReadExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	clone := make(map[string]any, len(extra))
	for key, value := range extra {
		clone[key] = value
	}
	return clone
}
func readExtraWithWindow(extra map[string]any, start, end int64) map[string]any {
	merged := cloneReadExtra(extra)
	if merged == nil {
		merged = map[string]any{}
	}
	merged["window_start"] = start
	merged["window_end"] = end
	return merged
}

// --- read_debug_state.go ---

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

// --- read_fast_path.go ---

func shouldPromoteCachedRange(requestSize int64) bool {
	return requestSize > 0 && requestSize < readChunkSize
}

func (v *VFS) hotChunk(cacheKey string, index int64) ([]byte, bool) {
	return newVFSReadRuntime(v).HotChunk(cacheKey, index)
}
func (v *VFS) putHotChunk(cacheKey string, index int64, data []byte) {
	newVFSReadRuntime(v).PutHotChunk(cacheKey, index, data)
}
func (v *VFS) debugHotChunks() (int, int64) {
	return newVFSReadRuntime(v).HotChunkStats()
}

// --- read_keys.go ---

func (v *VFS) readLoadKey(entry drive.Entry) string {
	if key := v.readCacheKey(entry); key != "" {
		return key
	}
	return entry.ID
}
func (v *VFS) readCacheKey(entry drive.Entry) string {
	if entry.ID == "" || entry.ModTime.IsZero() {
		return ""
	}
	sum := sha256.Sum256([]byte(v.rootID + "\x00" + entry.ID + "\x00" + strconv.FormatInt(entry.Size, 10) + "\x00" + strconv.FormatInt(entry.ModTime.UTC().UnixNano(), 10)))
	return hex.EncodeToString(sum[:])
}
func readChunkKey(fid string, index int64) string {
	return fid + "\x00" + strconv.FormatInt(index, 10)
}
func readWindowKey(fid string, start, end int64) string {
	return fid + "\x00" + strconv.FormatInt(start, 10) + "\x00" + strconv.FormatInt(end, 10)
}
