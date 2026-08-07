package vfs

import (
	"context"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/listing"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/read"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/vfstypes"
)

// StreamReader is the streaming read surface implemented by the VFS.
type StreamReader = read.StreamReader

// readState aliases the read domain state (internal/read).
type readState = read.State

// vfsReadHost adapts VFS internals to read.Host.
type vfsReadHost struct {
	v *VFS
}

func newVFSReadHost(v *VFS) vfsReadHost {
	return vfsReadHost{v: v}
}

func (h vfsReadHost) Resolve(ctx context.Context, path string) (drive.Entry, error) {
	return h.v.resolve(ctx, path)
}

func (h vfsReadHost) PendingUpload(path string) (vfstypes.PendingUpload, bool, error) {
	pending, err := h.v.pendingUpload(path)
	if err != nil {
		return vfstypes.PendingUpload{}, false, err
	}
	return pending, true, nil
}

func (h vfsReadHost) FlushStaging(localPath string) error {
	return h.v.uploads.Store().FlushStaging(localPath)
}

func (h vfsReadHost) RecordHealth(op string, err error) {
	h.v.recordHealthResult(op, err)
}

func (h vfsReadHost) ReadCacheKey(entry drive.Entry) string {
	return h.v.readCacheKey(entry)
}

func (h vfsReadHost) RootID() string {
	return h.v.rootID
}

func (h vfsReadHost) DriverRead(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	return h.v.driver.Read(ctx, entry, offset, size)
}

func (h vfsReadHost) DebugNextOpID() string {
	return h.v.nextDebugReadOpID()
}

func (h vfsReadHost) DebugBeginActive(op vfstypes.DebugActiveOp) uint64 {
	return h.v.beginDebugActive(op)
}

func (h vfsReadHost) DebugUpdateActive(id uint64, fn func(*vfstypes.DebugActiveOp)) {
	h.v.updateDebugActive(id, func(op *DebugActiveOp) { fn(op) })
}

func (h vfsReadHost) DebugFinishActive(id uint64) {
	h.v.finishDebugActive(id)
}

func (h vfsReadHost) DebugRecordRead(opID, path, remoteID string, offset, requested, bytes int64, source string, cacheHits, cacheMisses, chunks int64, started time.Time, extra map[string]any, err error) {
	h.v.recordDebugRead(opID, path, remoteID, offset, requested, bytes, source, cacheHits, cacheMisses, chunks, started, extra, err)
}

func (h vfsReadHost) DebugRecordReadDetail(ctx context.Context, path, remoteID, phase string, offset, requested, bytes int64, started time.Time, extra map[string]any, err error) {
	h.v.recordDebugReadDetail(ctx, path, remoteID, phase, offset, requested, bytes, started, extra, err)
}

func (h vfsReadHost) DebugCacheCounters() (hits, misses int64) {
	return h.v.debugCacheCounters()
}

// Read serves path content at offset/size, serving pending staging when
// present.
func (v *VFS) Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	return v.reader.Read(ctx, path, offset, size)
}

// ReadStream opens path for sequential streaming reads (bounded memory).
func (v *VFS) ReadStream(ctx context.Context, path string) (io.ReadCloser, error) {
	return v.reader.ReadStream(ctx, path)
}

// readCacheKey computes the stable cache key for an entry.
func (v *VFS) readCacheKey(entry drive.Entry) string {
	return read.CacheKey(v.rootID, entry)
}

// readLoadKey returns the window-coalescing key for an entry.
func (v *VFS) readLoadKey(entry drive.Entry) string {
	if key := v.readCacheKey(entry); key != "" {
		return key
	}
	return entry.ID
}

// readCacheSnapshot returns the read cache debug snapshot.
func (v *VFS) readCacheSnapshot() DebugReadCache {
	return v.read.DebugSnapshot()
}

// readCacheCounters returns the read cache hit/miss counters.
func (v *VFS) readCacheCounters() (hits, misses int64) {
	if v.read.Cache() == nil {
		return 0, 0
	}
	return v.read.Cache().Counters()
}

// debugHotChunks returns the hot-chunk entry count and bytes.
func (v *VFS) debugHotChunks() (int, int64) {
	return v.read.HotChunkStats()
}

// Read-prefetch context helpers and priority surface (implementations in
// internal/read).
var WithoutReadPrefetch = read.WithoutReadPrefetch
var WithoutDirPrefetch = listing.WithoutDirPrefetch

type ReadPriority = read.Priority

const (
	PriorityLow    = read.PriorityLow
	PriorityNormal = read.PriorityNormal
	PriorityHigh   = read.PriorityHigh
)

var WithReadPriority = read.WithPriority
