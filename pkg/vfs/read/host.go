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
// uploads, staging flush, driver reads, health, and debug bookkeeping.
// VFS implements it via vfsReadHost.
type Host interface {
	Resolve(ctx context.Context, path string) (drive.Entry, error)
	PendingUpload(path string) (vfstypes.PendingUpload, bool, error)
	FlushStaging(localPath string) error
	RecordHealth(op string, err error)
	ReadCacheKey(entry drive.Entry) string
	RootID() string
	DriverRead(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error)

	// Debug bookkeeping (implemented by the VFS debug layer).
	DebugNextOpID() string
	DebugBeginActive(op vfstypes.DebugActiveOp) uint64
	DebugUpdateActive(id uint64, fn func(*vfstypes.DebugActiveOp))
	DebugFinishActive(id uint64)
	DebugRecordRead(opID, path, remoteID string, offset, requested, bytes int64, source string, cacheHits, cacheMisses, chunks int64, started time.Time, extra map[string]any, err error)
	DebugRecordReadDetail(ctx context.Context, path, remoteID, phase string, offset, requested, bytes int64, started time.Time, extra map[string]any, err error)
	DebugCacheCounters() (hits, misses int64)
}

// Cache is the durable chunk store subset the read domain uses.
type Cache interface {
	AddHit()
	Close() error
	Counters() (hits, misses int64)
	FlushReadCache() error
	ClearReadCache() error
	InvalidateFile(fid string)
	PutLocalFile(fid string, fileSize int64, localPath string) error
	GetChunkRange(fid string, index, start, size int64) ([]byte, bool, error)
	GetChunkWithRange(fid string, index, start, size int64) ([]byte, []byte, bool, error)
	HasChunk(fid string, index int64) (bool, error)
	PutChunkAsync(fid string, fileSize, index int64, data []byte)
	DebugSnapshot() readcache.DebugReadCache
}

var _ Cache = (*readcache.Store)(nil)
