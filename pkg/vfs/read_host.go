package vfs

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/listing"
	"github.com/yinzhenyu/qrypt/pkg/vfs/observe"
	"github.com/yinzhenyu/qrypt/pkg/vfs/read"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// StreamReader is the streaming read surface implemented by the VFS.
type StreamReader = read.StreamReader

// ContextReader is the optional per-read cancellation surface for streams.
type ContextReader = read.ContextReader

// readState aliases the read domain state (internal/read).
type readState = read.State

// vfsReadHost adapts VFS internals to read.Host.
type vfsReadHost struct {
	resolver pathResolver
	driver   drive.Driver
	store    *uploadStore
	rootID   string
}

func newVFSReadHost(v *VFS) vfsReadHost {
	return vfsReadHost{resolver: v, driver: v.driver, store: v.uploads.Store(), rootID: v.rootID}
}

func (h vfsReadHost) Resolve(ctx context.Context, path string) (drive.Entry, error) {
	return h.resolver.resolve(ctx, path)
}

func (h vfsReadHost) PendingUpload(path string) (vfstypes.PendingUpload, bool, error) {
	pending, ok := h.store.UploadByPath(vfstypes.CleanVirtualPath(path))
	if !ok {
		return vfstypes.PendingUpload{}, false, fmt.Errorf("vfs: no pending file for %s", path)
	}
	return pending, true, nil
}

func (h vfsReadHost) FlushStaging(localPath string) error {
	return h.store.FlushStaging(localPath)
}

func (h vfsReadHost) ReadCacheKey(entry drive.Entry) string {
	return read.CacheKey(h.rootID, entry)
}

func (h vfsReadHost) DriverRead(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	return h.driver.Read(ctx, entry, offset, size)
}

// vfsReadHost adapts only the read host surface; debug instrumentation
// lives on vfsReadObserver and health statistics on vfsReadHealth, so the
// host contract stays free of diagnostic methods (enforced below).
var _ read.Host = vfsReadHost{}

// vfsReadHealth adapts the drive health tracker to read.HealthRecorder.
// It holds only the tracker - not the whole VFS - so the read adapter
// carries exactly the dependency it uses.
type vfsReadHealth struct {
	tracker *drive.HealthTracker
}

func (h vfsReadHealth) RecordResult(op string, err error) {
	h.tracker.RecordResult(op, err)
}

var _ read.HealthRecorder = vfsReadHealth{}

// vfsReadObserver adapts VFS debug internals to read.ReadObserver. It is a
// distinct type from vfsReadHost (even though both currently reach into the
// VFS) so the read host surface cannot grow debug methods by accident.
type vfsReadObserver struct {
	st     *readState
	active *observe.ActiveStore
}

func newVFSReadObserver(st *readState, active *observe.ActiveStore) vfsReadObserver {
	return vfsReadObserver{st: st, active: active}
}

func (o vfsReadObserver) DebugNextOpID() string {
	return newVFSDebugReadRuntime(o.st).NextOpID()
}

func (o vfsReadObserver) DebugBeginActive(op vfstypes.DebugActiveOp) uint64 {
	return o.active.Begin(op)
}

func (o vfsReadObserver) DebugUpdateActive(id uint64, fn func(*vfstypes.DebugActiveOp)) {
	o.active.Update(id, fn)
}

func (o vfsReadObserver) DebugFinishActive(id uint64) {
	o.active.Finish(id)
}

func (o vfsReadObserver) DebugRecordRead(opID, path, remoteID string, offset, requested, bytes int64, source string, cacheHits, cacheMisses, chunks int64, started time.Time, extra map[string]any, err error) {
	diagnostics.RecordRead(newVFSDebugReadRuntime(o.st), opID, path, remoteID, offset, requested, bytes, source, cacheHits, cacheMisses, chunks, started, extra, err)
}

func (o vfsReadObserver) DebugRecordReadDetail(ctx context.Context, path, remoteID, phase string, offset, requested, bytes int64, started time.Time, extra map[string]any, err error) {
	diagnostics.RecordReadDetail(newVFSDebugReadRuntime(o.st), ctx, path, remoteID, phase, offset, requested, bytes, started, extra, err)
}

func (o vfsReadObserver) DebugCacheCounters() (hits, misses int64) {
	return newVFSDebugReadRuntime(o.st).CacheCounters()
}

var _ read.ReadObserver = vfsReadObserver{}

// Read serves path content at offset/size, serving pending staging when
// present.
func (v *VFS) Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	return v.reader.Read(ctx, path, offset, size)
}

// ReleaseReadSession forgets adaptive read hints for a closed open-file
// handle. It leaves completed and in-flight cache fills intact.
func (v *VFS) ReleaseReadSession(sessionID uint64) {
	v.reader.ReleaseReadSession(sessionID)
}

type rawReadableDriver interface {
	ReadRaw(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error)
}

// ReadRaw opens the backend byte stream for path without read-cache or
// wrapper-level transforms. On encrypted mounts this returns ciphertext.
func (v *VFS) ReadRaw(ctx context.Context, path string, offset, size int64) (rc io.ReadCloser, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpRead, err) }()
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("vfs: raw read offset and size must be non-negative")
	}
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	if entry.IsDir {
		return nil, fmt.Errorf("vfs: %s is a directory", vfstypes.CleanVirtualPath(path))
	}
	if raw, ok := v.driver.(rawReadableDriver); ok {
		return raw.ReadRaw(ctx, entry, offset, size)
	}
	return v.driver.Read(ctx, entry, offset, size)
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
