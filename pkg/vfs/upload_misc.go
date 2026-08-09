package vfs

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"hash"
	"io"
	"os"
	"time"
)

// newUploadEngine builds the upload engine wired to VFS adapters.
func newUploadEngine(v *VFS) *upload.Engine {
	return upload.NewEngine(upload.EngineDeps{
		Remote:   newVFSDriverRuntime(v).RemoteMutationBackend(),
		Observer: newVFSUploadObserver(v),
		Pending:  upload.NewStoreAdapter(v.uploads.Store()),
		Runtime:  newVFSUploadRuntime(v),
		View:     newVFSViewCommitter(v),
		Snapshot: newVFSUploadSnapshotter(v),
		Faults:   newVFSUploadFaultController(v),
	})
}

// --- upload_fault.go ---

type vfsUploadFaultController struct {
	v *VFS
}

func newVFSUploadFaultController(v *VFS) vfsUploadFaultController {
	return vfsUploadFaultController{v: v}
}

func (c vfsUploadFaultController) ApplyCancelFault(ctx context.Context, pending PendingUpload, progress drive.UploadProgress, observer upload.Observer) (context.Context, drive.UploadProgress, func()) {
	fault, ok := c.v.matchUploadCancelFault(pending.Path, pending.FID)
	if !ok {
		return ctx, progress, nil
	}
	uploadCtx, uploadCancel := context.WithCancel(ctx)
	cancelProgress := &debugUploadCancelProgress{
		inner:      progress,
		fault:      fault,
		cancel:     uploadCancel,
		cancelPath: pending.Path,
		cancelOpID: pending.FID,
		v:          c.v,
	}
	observer.Extra(pending.Path, "debug_upload_cancel_fault", fault.ID)
	cleanup := func() {
		cancelProgress.Close()
		uploadCancel()
	}
	return uploadCtx, cancelProgress, cleanup
}

// --- upload_runtime.go ---

type vfsUploadRuntime struct {
	v *VFS
}

func newVFSUploadRuntime(v *VFS) vfsUploadRuntime {
	return vfsUploadRuntime{v: v}
}

func (r vfsUploadRuntime) ClearUploadHashes(fid string) {
	r.v.hashes.removeFID(fid)
}

func (r vfsUploadRuntime) RetryDelay(retryCount int) time.Duration {
	return upload.RetryDelay(retryCount, r.v.uploads.DefaultDelay())
}

func (r vfsUploadRuntime) Requeue(pending PendingUpload) {
	r.v.enqueue(pending)
}

func (r vfsUploadRuntime) RequeueIfFrozen(pending PendingUpload) {
	if pending.Frozen {
		r.Requeue(pending)
	}
}

// ModTimeFor returns the authoritative mtime for a pending upload (the
// local mod time set via SetModTime), or zero when the backend should use
// the upload time.
func (r vfsUploadRuntime) ModTimeFor(path string) time.Time {
	return r.v.localModTimeFor(path)
}

func (r vfsUploadRuntime) ApplyUploadModTime(pending PendingUpload, entry drive.Entry) drive.Entry {
	if modTime := r.v.localModTimeFor(pending.Path); !modTime.IsZero() {
		entry.ModTime = modTime
		return entry
	}
	if modTime := uploadModTime(pending); !modTime.IsZero() {
		entry.ModTime = modTime
		r.v.setLocalModTime(pending.Path, modTime)
	}
	return entry
}

type vfsUploadObserver struct {
	v *VFS
}

func newVFSUploadObserver(v *VFS) vfsUploadObserver {
	return vfsUploadObserver{v: v}
}

func (o vfsUploadObserver) Start(pending PendingUpload) {
	o.v.startUploadSnapshot(pending)
}

func (o vfsUploadObserver) State(path, state string) {
	o.v.setUploadSnapshotState(path, state)
}

func (o vfsUploadObserver) Extra(path, key string, value any) {
	o.v.setUploadSnapshotExtra(path, key, value)
}

func (o vfsUploadObserver) Metadata(path, resultRemoteID string, hashes []string) {
	o.v.setUploadSnapshotMetadata(path, resultRemoteID, hashes)
}

func (o vfsUploadObserver) Event(path, phase string, start time.Time, bytes int64, extra map[string]any) {
	o.v.recordUploadEvent(path, phase, start, bytes, extra)
}

func (o vfsUploadObserver) Uploaded(path string, n int) {
	o.v.updateUploadSnapshot(path, n)
}

func (o vfsUploadObserver) HealthResult(op string, err error) {
	o.v.healthTracker.RecordResult(op, err)
}

func (o vfsUploadObserver) Finish(path, state, lastError string) {
	o.v.finishUploadSnapshot(path, state, lastError)
}

// --- upload_snapshot.go ---

type vfsUploadSnapshotter struct {
	v *VFS
}

func newVFSUploadSnapshotter(v *VFS) vfsUploadSnapshotter {
	return vfsUploadSnapshotter{v: v}
}

func (s vfsUploadSnapshotter) SnapshotPending(pending PendingUpload) (upload.Snapshot, error) {
	return s.v.snapshotPending(pending)
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
	algorithms := v.requiredUploadSnapshotHashes()
	if hashes, ok := v.hashes.snapshot(pending, algorithms); ok {
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
	hashes, writers, err := newUploadSnapshotHashes(algorithms)
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

func (v *VFS) requiredUploadSnapshotHashes() []drive.HashAlgorithm {
	required := []drive.HashAlgorithm{drive.HashSHA256}
	if v != nil {
		required = append(required, newVFSDriverRuntime(v).RequiredUploadHashes()...)
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

func newUploadSnapshotHashes(algorithms []drive.HashAlgorithm) (map[drive.HashAlgorithm]hash.Hash, []io.Writer, error) {
	hashes := make(map[drive.HashAlgorithm]hash.Hash, len(algorithms))
	writers := make([]io.Writer, 0, len(algorithms))
	for _, algorithm := range algorithms {
		var h hash.Hash
		switch algorithm {
		case drive.HashMD5:
			h = md5.New()
		case drive.HashSHA1:
			h = sha1.New()
		case drive.HashSHA256:
			h = sha256.New()
		default:
			return nil, nil, fmt.Errorf("vfs: unsupported upload hash algorithm %q", algorithm)
		}
		hashes[algorithm] = h
		writers = append(writers, h)
	}
	return hashes, writers, nil
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
	v *VFS
}

func newVFSUploadWorkerRuntime(v *VFS) vfsUploadWorkerRuntime {
	return vfsUploadWorkerRuntime{v: v}
}

func (r vfsUploadWorkerRuntime) Receive(ctx context.Context) (PendingUpload, bool) {
	select {
	case <-ctx.Done():
		return PendingUpload{}, false
	case pending := <-r.v.uploads.Queue():
		return pending, true
	}
}

func (r vfsUploadWorkerRuntime) StopUploadTimers() {
	r.v.uploads.Close()
}

func (r vfsUploadWorkerRuntime) StopDeleteTimers() {
	r.v.deletes.Close()
}

func (r vfsUploadWorkerRuntime) SourceUploadSupported() bool {
	return newVFSDriverRuntime(r.v).HasCapability(drive.CapabilitySourceUploader)
}

func (r vfsUploadWorkerRuntime) LatestUpload(path string) (PendingUpload, bool) {
	return r.v.uploads.Store().UploadByPath(path)
}

func (r vfsUploadWorkerRuntime) RemoveStagingIfUnreferenced(localPath string) {
	r.v.uploads.Store().RemoveStagingIfUnreferenced(localPath)
}

func (r vfsUploadWorkerRuntime) Requeue(pending PendingUpload) {
	r.v.enqueue(pending)
}

func (r vfsUploadWorkerRuntime) RequeueAfter(pending PendingUpload, delay time.Duration) {
	r.v.enqueueAfter(pending, delay)
}

func (r vfsUploadWorkerRuntime) QuietDelay(pending PendingUpload) time.Duration {
	d := r.v.uploadQuietDelay(pending)
	return d
}

func (r vfsUploadWorkerRuntime) QuietWindow(pending PendingUpload) time.Duration {
	return r.v.uploadQuietWindow(pending)
}

func (r vfsUploadWorkerRuntime) TryAcquire(pending PendingUpload, workers int) bool {
	return r.v.uploads.TryAcquire(pending, workers)
}

func (r vfsUploadWorkerRuntime) Release(pending PendingUpload) {
	r.v.uploads.Release(pending)
}

func (r vfsUploadWorkerRuntime) ExecuteUpload(ctx context.Context, pending PendingUpload) error {
	return newUploadEngine(r.v).Execute(ctx, pending)
}

func (r vfsUploadWorkerRuntime) SendUpload(pending PendingUpload) {
	r.v.uploads.Enqueue(pending)
}

// enqueueBlocking sends pending to the upload queue, blocking until the
// record is delivered or the VFS shuts down; returns true when delivered.
func (r vfsUploadWorkerRuntime) enqueueBlocking(pending PendingUpload) bool {
	select {
	case r.v.uploads.Queue() <- pending:
		return true
	case <-r.v.done:
		return false
	}
}

// Compile-time check that the adapter satisfies upload.WorkerRuntime.
var _ upload.WorkerRuntime = (*vfsUploadWorkerRuntime)(nil)
