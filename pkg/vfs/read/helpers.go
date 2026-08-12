package read

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

type readPrefetchContextKey struct{}

func WithoutReadPrefetch(ctx context.Context) context.Context {
	return context.WithValue(ctx, readPrefetchContextKey{}, true)
}

func readPrefetchEnabled(ctx context.Context) bool {
	disabled, _ := ctx.Value(readPrefetchContextKey{}).(bool)
	return !disabled
}

// Priority controls how read slots are allocated under contention.
type Priority int

const (
	PriorityLow    Priority = iota // prefetch, anticipatory reads
	PriorityNormal                 // default user-initiated reads
	PriorityHigh                   // UI-critical reads (thumbnails, visible content)
)

type priorityKey struct{}

func WithPriority(ctx context.Context, p Priority) context.Context {
	return context.WithValue(ctx, priorityKey{}, p)
}

func priority(ctx context.Context) Priority {
	p, _ := ctx.Value(priorityKey{}).(Priority)
	return p
}

// --- keys ---

func ChunkKey(fid string, index int64) string {
	return fid + "\x00" + strconv.FormatInt(index, 10)
}

func WindowKey(fid string, start, end int64) string {
	return fid + "\x00" + strconv.FormatInt(start, 10) + "\x00" + strconv.FormatInt(end, 10)
}

// LoadKey returns the window-coalescing key for an entry: the read cache
// key when available, otherwise the remote id.
func LoadKey(host Host, entry drive.Entry) string {
	if key := host.ReadCacheKey(entry); key != "" {
		return key
	}
	return entry.ID
}

// CacheKey computes the read cache key for an entry (stable across mounts
// with the same root id, entry id, size, and mod time).
func CacheKey(rootID string, entry drive.Entry) string {
	if entry.ID == "" || entry.ModTime.IsZero() {
		return ""
	}
	sum := sha256.Sum256([]byte(rootID + "\x00" + entry.ID + "\x00" + strconv.FormatInt(entry.Size, 10) + "\x00" + strconv.FormatInt(entry.ModTime.UTC().UnixNano(), 10)))
	return hex.EncodeToString(sum[:])
}

func DurationMillis(d time.Duration) int64 { return d.Milliseconds() }

func WindowExtra(windowChunks int) map[string]any {
	return map[string]any{
		"window_chunks": windowChunks,
	}
}

func ExtraWithWindow(extra map[string]any, start, end int64) map[string]any {
	merged := CloneExtra(extra)
	if merged == nil {
		merged = map[string]any{}
	}
	merged["window_start"] = start
	merged["window_end"] = end
	return merged
}

func CloneExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	clone := make(map[string]any, len(extra))
	for key, value := range extra {
		clone[key] = value
	}
	return clone
}

func shouldPromoteCachedRange(requestSize int64) bool {
	return requestSize > 0 && requestSize < ChunkSize
}

func WindowBytes(chunks map[int64][]byte) int64 {
	var total int64
	for _, chunk := range chunks {
		total += int64(len(chunk))
	}
	return total
}

// DebugOperationName returns the debug op name from ctx, if any.
func DebugOperationName(ctx context.Context) string {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok {
		return ""
	}
	return op.Name
}

// RecordChunkDetail records a per-chunk debug event when ctx carries a
// debug operation.
func RecordChunkDetail(observer ReadObserver, ctx context.Context, entry drive.Entry, phase string, index, start, size, bytes int64, started time.Time, extra map[string]any, err error) {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok || op.OpID == "" {
		return
	}
	if extra == nil {
		extra = map[string]any{}
	}
	extra["chunk_index"] = index
	extra["chunk_offset"] = index * ChunkSize
	extra["chunk_range_start"] = start
	extra["chunk_range_size"] = size
	observer.DebugRecordReadDetail(ctx, op.Name, entry.ID, phase, index*ChunkSize+start, size, bytes, started, extra, err)
}

// CacheLookupExtra annotates extra with the cache lookup source and time.
func CacheLookupExtra(source string, started time.Time, extra map[string]any) map[string]any {
	out := CloneExtra(extra)
	if out == nil {
		out = map[string]any{}
	}
	out["cache_lookup_source"] = source
	out["cache_lookup_ms"] = DurationMillis(util.Now().Sub(started))
	return out
}
