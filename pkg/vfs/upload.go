package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
	"github.com/yinzhenyu/qrypt/pkg/vfs/pathlock"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
	"io"
	"os"
	"time"
)

// Upload domain shell on VFS: type aliases into pkg/vfs/upload, the
// service/engine assembly, and the VFS upload methods (scheduling,
// snapshotting, staging read-cache seeds, worker loop).
type uploadService = upload.Service
type uploadStore = upload.PendingStore
type uploadTaskRecord = upload.UploadTaskRecord
type uploadSnapshot = upload.UploadSnapshot
type uploadSnapshotState = upload.SnapshotState
type uploadAdmission = upload.Admission
type uploadTargetIndex = upload.TargetIndex

func newUploadTargetIndex() *uploadTargetIndex {
	return upload.NewTargetIndex(upload.DefaultTargetIndexTTL)
}

// newUploadService builds the upload service wired to VFS state.
func newUploadService(store *uploadStore, opts Options, done chan struct{}, hashes *upload.HashTracker) *uploadService {
	return upload.NewService(upload.ServiceOptions{
		UploadDelay:   opts.UploadDelay,
		UploadWorkers: opts.UploadWorkers,
		Store:         store,
		Done:          done,
		HashOps:       hashes,
	})
}

// timeFromUnixNano converts unix nanos to a time.Time (zero when <= 0).
var timeFromUnixNano = upload.TimeFromUnixNano

// PendingUploads returns all pending uploads known to this VFS.
func (v *VFS) PendingUploads() []PendingUpload {
	return v.uploads.PendingUploads()
}

var largeUploadQuietThreshold int64 = upload.LargeUploadQuietThreshold
var largeUploadQuietDelay = upload.LargeUploadQuietDelay

// VFS wrapper methods delegating to uploadService. These exist for
// backward compatibility with internal callers; the upload schedule
// implementation lives in internal/upload.

func (v *VFS) enqueue(p PendingUpload) {
	if v.uploadSchedulingEnabled() {
		v.uploads.Enqueue(p)
	}
}

func (v *VFS) enqueueAfter(p PendingUpload, d time.Duration) bool {
	if !v.uploadSchedulingEnabled() {
		return false
	}
	v.uploads.EnqueueAfter(p, d)
	return true
}

// before Start and Resume can enqueue the same generation again.
func (v *VFS) uploadSchedulingEnabled() bool {
	v.lifecycleMu.Lock()
	defer v.lifecycleMu.Unlock()
	return v.lifecycle == lifecycleRunning
}

func (v *VFS) uploadQuietWindow(p PendingUpload) time.Duration { return v.uploads.QuietWindow(p) }

// newUploadEngine builds the upload engine wired to VFS adapters.
func newUploadEngine(v *VFS) *upload.Engine {
	return upload.NewEngine(upload.EngineDeps{
		Remote:        newVFSDriverRuntime(v).RemoteMutationBackend(),
		Targets:       v.uploadTargets,
		Observer:      newVFSUploadObserver(v.uploads, v.healthTracker),
		Pending:       upload.NewStoreAdapter(v.uploads.Store()),
		Runtime:       newVFSUploadRuntime(v),
		View:          newVFSViewCommitter(v),
		Invalidations: vfsUploadInvalidations{invalidations: &v.invalidations},
		Snapshot:      newVFSUploadSnapshotter(v),
		Faults:        newVFSUploadFaultController(v.faults),
	})
}

type vfsUploadInvalidations struct {
	invalidations *invalidationState
}

// --- upload_fault.go ---

type vfsUploadFaultController struct {
	faults *faultinject.Registry
}

// --- upload_runtime.go ---

type vfsUploadRuntime struct {
	hashes            *upload.HashTracker
	uploads           *uploadService
	viewRT            view.Runtime
	schedulingEnabled func() bool
}

// the upload time.
func (r vfsUploadRuntime) ModTimeFor(path string) time.Time {
	return r.viewRT.LocalModTimeFor(path)
}

type vfsUploadObserver struct {
	svc     *uploadService
	tracker *drive.HealthTracker
}

// --- upload_snapshot.go ---

type vfsUploadSnapshotter struct {
	locks           *pathlock.State
	store           *uploadStore
	hashes          *upload.HashTracker
	driver          drive.Driver
	snapshotPending func(PendingUpload) (upload.Snapshot, error)
}

func (v *VFS) snapshotPending(pending PendingUpload) (upload.Snapshot, error) {
	unlock := v.lockPath(pending.Path)
	defer unlock()
	if err := v.uploads.Store().SyncStaging(pending.LocalPath); err != nil {
		return upload.Snapshot{}, err
	}
	info, err := os.Stat(pending.LocalPath)
	if err != nil {
		return upload.Snapshot{}, err
	}
	if info.Size() != pending.Size {
		return upload.Snapshot{}, fmt.Errorf("vfs: pending changed during upload snapshot: file has %d, expected %d", info.Size(), pending.Size)
	}
	algorithms := requiredUploadSnapshotHashes(v.driver)
	if hashes, ok := v.hashes.Snapshot(pending, algorithms); ok {
		return upload.Snapshot{
			Path:        pending.LocalPath,
			Hashes:      hashes,
			Incremental: true,
		}, nil
	}
	src, err := os.Open(pending.LocalPath)
	if err != nil {
		return upload.Snapshot{}, err
	}
	defer src.Close()
	hashes, writers, err := upload.NewSnapshotHashes(algorithms)
	if err != nil {
		return upload.Snapshot{}, err
	}
	if _, err := io.Copy(io.MultiWriter(writers...), src); err != nil {
		return upload.Snapshot{}, err
	}
	sums := make(drive.SourceHashes, len(hashes))
	for algorithm, h := range hashes {
		sums[algorithm] = h.Sum(nil)
	}
	return upload.Snapshot{
		Path:   pending.LocalPath,
		Hashes: sums,
	}, nil
}

// requiredUploadSnapshotHashes returns the hash algorithms an upload snapshot
// must compute: SHA-256 plus whatever the driver requires.
func requiredUploadSnapshotHashes(driver drive.Driver) []drive.HashAlgorithm {
	required := []drive.HashAlgorithm{drive.HashSHA256}
	if driver != nil {
		required = append(required, driver.RequiredUploadHashes()...)
	}
	seen := make(map[drive.HashAlgorithm]bool, len(required))
	algorithms := make([]drive.HashAlgorithm, 0, len(required))
	for _, algorithm := range required {
		if algorithm == "" || seen[algorithm] {
			continue
		}
		seen[algorithm] = true
		algorithms = append(algorithms, algorithm)
	}
	return algorithms
}

func (v *VFS) seedReadCacheFromStaging(entry drive.Entry, localPath string) {
	cacheKey := v.readCacheKey(entry)
	if cacheKey == "" || localPath == "" {
		return
	}
	if entry.Size >= readCacheLargeFileBytes {
		logging.L.DebugfEvery("vfs.read_cache_seed_skip_large", time.Second, "[VFS] skip read cache seed for large upload id=%q size=%d local=%q", entry.ID, entry.Size, localPath)
		return
	}
	if err := v.read.PutLocalFile(cacheKey, entry.Size, localPath); err != nil {
		logging.L.Warnf("[VFS] read cache seed failed id=%q local=%q err=%v", entry.ID, localPath, err)
	}
}

func (v *VFS) seedReadCacheFromSource(ctx context.Context, entry drive.Entry, source drive.ReadOnlyFileSource) {
	cacheKey := v.readCacheKey(entry)
	if cacheKey == "" || source == nil {
		return
	}
	if entry.Size >= readCacheLargeFileBytes {
		logging.L.DebugfEvery("vfs.read_cache_seed_source_skip_large", time.Second, "[VFS] skip read cache seed for large direct upload id=%q size=%d", entry.ID, entry.Size)
		return
	}
	reader, err := source.Open(ctx)
	if err != nil {
		logging.L.Warnf("[VFS] direct upload read cache source open failed id=%q err=%v", entry.ID, err)
		return
	}
	defer reader.Close()
	if err := v.read.PutReader(cacheKey, entry.Size, reader); err != nil {
		logging.L.Warnf("[VFS] direct upload read cache seed failed id=%q err=%v", entry.ID, err)
	}
}

// --- upload_worker.go ---

func (v *VFS) uploadWorker(ctx context.Context) {
	runtime := newVFSUploadWorkerRuntime(v)
	for {
		pending, ok := runtime.Receive(ctx)
		if !ok {
			runtime.StopUploadTimers()
			runtime.StopDeleteTimers()
			return
		}
		_ = v.uploadPending(ctx, pending)
	}
}

func (v *VFS) uploadPending(ctx context.Context, pending PendingUpload) error {
	return upload.PendingWithRuntime(ctx, pending, newVFSUploadWorkerRuntime(v), v.uploads.WorkerCount(), v.uploads.DefaultDelay())
}

// vfsUploadWorkerRuntime adapts VFS internals to upload.WorkerRuntime.
type vfsUploadWorkerRuntime struct {
	uploads           *uploadService
	deletes           *DeleteService
	driver            drive.Driver
	done              chan struct{}
	schedulingEnabled func() bool
	engine            *upload.Engine
}

// enqueueBlocking delivers a pending to the worker queue, blocking until it
// is accepted or the VFS shuts down; reports whether it was delivered.
func (r vfsUploadWorkerRuntime) enqueueBlocking(pending PendingUpload) bool {
	select {
	case r.uploads.Queue() <- pending:
		return true
	case <-r.done:
		return false
	}
}

// Compile-time check that the adapter satisfies upload.WorkerRuntime.
var _ upload.WorkerRuntime = (*vfsUploadWorkerRuntime)(nil)

func pendingUploadFromWriteStore(store *uploadStore, path string) (PendingUpload, error) {
	path = vfstypes.CleanVirtualPath(path)
	pending, ok := store.UploadByPath(path)
	if !ok {
		return PendingUpload{}, fmt.Errorf("vfs: no pending file for %s", path)
	}
	return pending, nil
}

type vfsUploadWriteHashTracker struct {
	hashes *upload.HashTracker
	driver drive.Driver
}

type vfsUploadWriteRemote struct {
	resolver    pathResolver
	driver      drive.Driver
	invalidator readCacheInvalidator
}
