// Package read implements the VFS read domain: chunked range reads,
// window coalescing, the hot-chunk fast path, and sequential streaming.
// It is driven by a Host interface so it stays free of VFS internals.
package read

import (
	"context"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Host is the VFS surface the read domain needs: resolution, pending
// uploads, staging flush, driver reads, and the read-cache key derivation.
// Health statistics and debug bookkeeping are NOT part of the host surface
// - they live on the optional HealthRecorder and ReadObserver so the
// hot-path contract stays narrow. VFS implements it via vfsReadHost.
//
// Cache keys are derived in the read domain (CacheKey) but need the VFS's
// root ID, so the host exposes the finished CacheKey(entry) instead of raw
// identity fields.
type Host interface {
	Resolve(ctx context.Context, path string) (drive.Entry, error)
	PendingUpload(path string) (vfstypes.PendingUpload, bool, error)
	FlushStaging(localPath string) error
	ReadCacheKey(entry drive.Entry) string
	DriverRead(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error)
}

// HealthRecorder receives read-domain health statistics. It is optional:
// the read domain works without one (a no-op sink), and VFS wires its
// health tracker through it so statistics never widen the host surface.
type HealthRecorder interface {
	RecordResult(op string, err error)
}

// noopHealth is the default HealthRecorder.
type noopHealth struct{}

func (noopHealth) RecordResult(string, error) {}

// ReadObserver receives read-domain debug bookkeeping. It is optional: the
// read domain works without one (a no-op sink), and VFS wires its debug
// layer through it. Keeping it separate from Host means debug requirements
// cannot grow the host surface the hot path depends on.
type ReadObserver interface {
	DebugNextOpID() string
	DebugBeginActive(op vfstypes.DebugActiveOp) uint64
	DebugUpdateActive(id uint64, fn func(*vfstypes.DebugActiveOp))
	DebugFinishActive(id uint64)
	DebugRecordRead(opID, path, remoteID string, offset, requested, bytes int64, source string, cacheHits, cacheMisses, chunks int64, started time.Time, extra map[string]any, err error)
	DebugRecordReadDetail(ctx context.Context, path, remoteID, phase string, offset, requested, bytes int64, started time.Time, extra map[string]any, err error)
	DebugCacheCounters() (hits, misses int64)
}

// noopObserver is the default ReadObserver: the read domain is fully
// functional without a debug sink, and tests that stub only Host get it
// automatically.
type noopObserver struct{}

func (noopObserver) DebugNextOpID() string { return "" }
func (noopObserver) DebugBeginActive(vfstypes.DebugActiveOp) uint64 {
	return 0
}
func (noopObserver) DebugUpdateActive(uint64, func(*vfstypes.DebugActiveOp)) {}
func (noopObserver) DebugFinishActive(uint64)                                {}
func (noopObserver) DebugRecordRead(opID, path, remoteID string, offset, requested, bytes int64, source string, cacheHits, cacheMisses, chunks int64, started time.Time, extra map[string]any, err error) {
}
func (noopObserver) DebugRecordReadDetail(ctx context.Context, path, remoteID, phase string, offset, requested, bytes int64, started time.Time, extra map[string]any, err error) {
}
func (noopObserver) DebugCacheCounters() (hits, misses int64) { return 0, 0 }

// Cache is the durable chunk store subset the read domain uses.
type Cache interface {
	AddHit()
	Close() error
	Counters() (hits, misses int64)
	FlushReadCache() error
	ClearReadCache() error
	InvalidateFile(fid string)
	PutLocalFile(fid string, fileSize int64, localPath string) error
	PutReader(fid string, fileSize int64, r io.Reader) error
	GetChunkRange(fid string, index, start, size int64) ([]byte, bool, error)
	GetChunkWithRange(fid string, index, start, size int64) ([]byte, []byte, bool, error)
	HasChunk(fid string, index int64) (bool, error)
	PutChunkAsync(fid string, fileSize, index int64, data []byte)
	DebugSnapshot() readcache.DebugReadCache
}

var _ Cache = (*readcache.Store)(nil)
