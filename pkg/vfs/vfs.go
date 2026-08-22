// Package vfs provides the platform-independent file API used by CLI, FUSE,
// and future mobile adapters.
package vfs

import (
	"context"
	"errors"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
	"github.com/yinzhenyu/qrypt/pkg/vfs/listing"
	"github.com/yinzhenyu/qrypt/pkg/vfs/observe"
	"github.com/yinzhenyu/qrypt/pkg/vfs/read"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const uploadDebounceDelay = 5 * time.Second
const zeroByteUploadDebounceDelay = 10 * time.Second
const defaultUploadWorkers = 4
const deleteDebounceDelay = 2 * time.Second
const restoredDirTTL = 60 * time.Second
const directoryCopyHideTTL = 10 * time.Minute
const localCreateLookupTTL = 2 * time.Minute

type Options struct {
	Name          string
	StorageDir    string
	ReadCacheDir  string
	UploadDir     string
	CacheMaxBytes int64
	RootID        string
	Encrypted     bool
	TestEnabled   bool
	UploadDelay   time.Duration
	UploadWorkers int
	DeleteDelay   time.Duration
}

type VFS struct {
	driver        drive.Driver
	name          string
	healthTracker *drive.HealthTracker
	rootID        string
	encrypted     bool
	testEnabled   bool

	// Domain state is grouped by ownership: each runtime is initialized in
	// New, mutated only by its domain's code paths, and shut down by the VFS
	// lifecycle. activeDebug (single-state) and pathLocks (cross-domain)
	// stay top-level.
	view          *viewState
	read          *readState
	reader        *read.Reader
	uploads       *uploadService
	uploadTargets *uploadTargetIndex
	deletes       *DeleteService
	listing       *listingState
	lister        *listing.Lister
	hashes        *uploadHashTrackerState
	// activeDebug tracks in-flight debug operations; it is the debug
	// domain's only top-level state (read history and upload debug live in
	// their domains).
	activeDebug   *observe.ActiveStore
	faults        *faultinject.Registry
	pathLocks     *pathLockState
	invalidations invalidationState

	// done is closed when the VFS shuts down (Close or context cancel in
	// Start). The blocking upload-queue enqueue goroutine selects on it so
	// it cannot leak after the upload workers have exited.
	done chan struct{}
	// ctx is the lifecycle context from Start, kept so background tasks
	// (remote deletes, prefetch) derive from the owning context instead of
	// context.Background(). Stored atomically in Start: background tasks
	// (e.g. delayed-delete timers) read it without holding lifecycleMu.
	ctx atomic.Pointer[context.Context]
	// lifecycleMu serializes Start/Close state transitions; lifecycle
	// tracks the current phase. Start registers every worker while holding
	// the lock and teardown flips the state to closing under it, so a
	// concurrent Start can never add workers after teardown began waiting.
	lifecycleMu sync.Mutex
	lifecycle   lifecycleState
	// cancel cancels the lifecycle context; Close calls it first so upload
	// workers exit and in-flight remote operations abort. Nil until Start.
	cancel context.CancelFunc
	// workerWG tracks the upload workers started by Start. Close waits for
	// it so no worker outlives the VFS.
	workerWG sync.WaitGroup
	// closeOnce makes Close idempotent; closeErr holds the first Close's
	// error (nil until the first Close completes) and is safe to read after
	// any Close call returns.
	closeOnce sync.Once
	closeErr  error
	// closeDone is closed when Close has finished tearing down background
	// workers; callers and tests can wait on it.
	closeDone chan struct{}
}

type lifecycleState uint8

const (
	lifecycleNew lifecycleState = iota
	lifecycleRunning
	lifecycleClosing
	lifecycleClosed
)

type overlayOp struct {
	oldPath string
	newPath string
	entryID string
	isDir   bool
	oldGone bool
	newSeen bool
}

func New(driver drive.Driver, opts Options) (*VFS, error) {
	if opts.Name == "" {
		opts.Name = "default"
	}
	if opts.RootID == "" {
		opts.RootID = "0"
	}
	if opts.UploadDelay == 0 {
		opts.UploadDelay = uploadDebounceDelay
	}
	if opts.UploadWorkers <= 0 {
		opts.UploadWorkers = defaultUploadWorkers
	}
	if opts.DeleteDelay == 0 {
		opts.DeleteDelay = deleteDebounceDelay
	}
	readCacheDir := opts.ReadCacheDir
	uploadDir := opts.UploadDir
	if readCacheDir == "" {
		readCacheDir = filepath.Join(opts.StorageDir, "reading")
	}
	if uploadDir == "" {
		uploadDir = opts.StorageDir
	}
	stores, err := newStores(uploadDir, readCacheDir, opts.CacheMaxBytes)
	if err != nil {
		return nil, err
	}
	now := util.Now()
	overlay, deleteTasks := newDeleteStates()
	view := newViewState(opts.RootID, now)
	view.overlay = overlay
	done := make(chan struct{})
	hashes := newUploadHashTrackerState()
	v := &VFS{
		driver:        driver,
		name:          opts.Name,
		healthTracker: drive.NewHealthTracker(drive.DefaultHealthWindow, drive.DefaultMaxEvents),
		rootID:        opts.RootID,
		encrypted:     opts.Encrypted,
		testEnabled:   opts.TestEnabled,
		done:          done,
		view:          view,
		read:          read.NewState(stores.readCacheStore),
		hashes:        hashes,
		uploads:       newUploadService(stores.uploadStore, opts, done, hashes),
		uploadTargets: newUploadTargetIndex(),
		deletes:       newDeleteService(deleteTasks, opts.DeleteDelay),
		listing:       listing.NewState(),
		activeDebug:   observe.NewActiveStore(opts.Name),
		faults:        faultinject.NewRegistry(0),
		pathLocks:     newPathLockState(),
		closeDone:     make(chan struct{}),
	}
	v.reader = read.NewReader(read.ReaderDeps{
		Host:     newVFSReadHost(v),
		State:    v.read,
		Observer: newVFSReadObserver(v),
		Health:   vfsReadHealth{tracker: v.healthTracker},
	})
	v.lister = listing.NewLister(listing.ListerDeps{
		Remote: newVFSListingRemote(v),
		View:   newVFSListingView(v),
		State:  v.listing,
		Health: vfsListingHealth{tracker: v.healthTracker},
	})
	return v, nil
}

// Start launches the upload workers and resumes pending uploads.
//
// Lifecycle ownership: the FIRST context passed to Start owns the VFS
// lifecycle. Later Start calls are no-ops - they do not start a second set
// of workers, do not re-run resume (which would double-schedule pending
// uploads) and do not register a second shutdown hook. A different context
// passed to a second Start is ignored: cancelling it does not stop the
// VFS. Once Start has been called, the instance runs until Close (or the
// owning context cancelling, which triggers Close) - build a new VFS to
// restart.
func (v *VFS) Start(ctx context.Context) {
	v.lifecycleMu.Lock()
	if v.lifecycle != lifecycleNew {
		// Already running, or teardown already began/ended: never start a
		// second set of workers, and never start workers on an instance
		// being (or already) closed.
		v.lifecycleMu.Unlock()
		return
	}
	v.lifecycle = lifecycleRunning
	ctx, cancel := context.WithCancel(ctx)
	v.ctx.Store(&ctx)
	v.cancel = cancel
	// Register every worker under the lifecycle lock so a concurrent
	// teardown that flips the state to closing and starts waiting sees a
	// complete WaitGroup (no Add after Wait).
	for i := 0; i < v.uploads.WorkerCount(); i++ {
		v.workerWG.Add(1)
		go func() {
			defer v.workerWG.Done()
			v.uploadWorker(ctx)
		}()
	}
	v.lifecycleMu.Unlock()

	// Resume only while still running: a concurrent Close that already
	// flipped the state would otherwise receive new scheduled work it no
	// longer owns. (The upload service's own stopped flag makes the worst
	// case a discarded timer, never a leak.)
	v.lifecycleMu.Lock()
	running := v.lifecycle == lifecycleRunning
	v.lifecycleMu.Unlock()
	if running {
		v.Resume(ctx)
	}
	// Graceful stop: when the owning context is cancelled (or Close is
	// called directly), stop the upload/delete timers, wait for the
	// upload workers, and close the read-cache writer so no background
	// goroutine outlives the VFS. Close is idempotent, so an explicit
	// Close call racing this hook is safe.
	context.AfterFunc(ctx, func() { _ = v.Close(context.Background()) })
}

// Close shuts down the VFS and waits for all background work to finish.
//
// It is idempotent: the first call starts the teardown and later calls
// return the same result. Close can be called before Start (it tears down
// construction-time resources such as the read-cache writer) and after the
// owning context was cancelled (the Start hook calls Close itself), and it
// can race Start or the context-cancel hook safely: Start and Close are
// serialized by the lifecycle lock, so Close never tears down under a
// Start that is still adding workers.
//
// Close stops accepting new background work, cancels the lifecycle context
// (upload workers exit, in-flight remote operations abort), stops
// upload/delete timers, waits for upload workers and background enqueue
// goroutines to exit, and closes the read-cache writer, flushing queued
// writes. Teardown runs in a single background flow: Close returns when it
// completes, or early with ctx.Err() if ctx is done first - the teardown
// keeps running and a later Close call waits for it to finish.
func (v *VFS) Close(ctx context.Context) error {
	v.closeOnce.Do(func() {
		go v.teardown()
	})
	select {
	case <-v.closeDone:
		return v.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// teardown is the single background flow that releases all VFS resources.
// It is started once by the first Close call and always runs to completion;
// Close callers may time out and return early, but teardown is never
// aborted.
func (v *VFS) teardown() {
	v.lifecycleMu.Lock()
	switch v.lifecycle {
	case lifecycleNew:
		// Never started: only the construction-time read-cache writer needs
		// closing; nothing to cancel or wait on.
		v.lifecycle = lifecycleClosed
		v.lifecycleMu.Unlock()
		var errs []error
		if err := v.read.Close(); err != nil {
			errs = append(errs, err)
		}
		v.closeErr = errors.Join(errs...)
		close(v.closeDone)
		return
	case lifecycleRunning:
		// Flip to closing under the lock: a concurrent Start sees non-New
		// and cannot Add workers after we start waiting.
		v.lifecycle = lifecycleClosing
		v.lifecycleMu.Unlock()
	case lifecycleClosing, lifecycleClosed:
		// closeOnce makes teardown single-flight; this is defensive.
		v.lifecycleMu.Unlock()
		return
	}

	if v.cancel != nil {
		v.cancel()
	}
	v.uploads.Close()
	v.deletes.Close()
	close(v.done)
	v.workerWG.Wait()
	v.uploads.Wait()
	var errs []error
	if err := v.read.Close(); err != nil {
		errs = append(errs, err)
	}
	v.closeErr = errors.Join(errs...)
	v.lifecycleMu.Lock()
	v.lifecycle = lifecycleClosed
	v.lifecycleMu.Unlock()
	close(v.closeDone)
}

func (v *VFS) StartDirectoryPrefetch(ctx context.Context) {
	if !v.startDirPrefetch(ctx) {
		return
	}

	go func() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		start := time.Now()
		entries, err := v.listNoPrefetch(ctx, "/")
		if err != nil {
			if ctx.Err() == nil {
				logging.L.DebugfEvery("vfs.dir_prefetch_root_failed", time.Second, "[PREFETCH] root list failed path=%q dur=%s err=%v", "/", time.Since(start), err)
			}
			return
		}
		logging.L.DebugfEvery("vfs.dir_prefetch_root_complete", time.Second, "[PREFETCH] root list complete path=%q entries=%d dur=%s", "/", len(entries), time.Since(start))
		v.scheduleDirPrefetch(ctx, "/", entries)
	}()
}

func (v *VFS) Stat(ctx context.Context, path string) (entry drive.Entry, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpStat, err) }()
	path = cleanVirtual(path)
	if pending, err := v.pendingUpload(path); err == nil {
		entry := drive.Entry{
			ID:        pending.FID,
			ParentID:  pending.ParentID,
			Name:      pending.Name,
			IsDir:     false,
			Size:      pending.Size,
			ModTime:   uploadModTime(pending),
			UpdatedAt: uploadModTime(pending),
		}
		return v.applyLocalModTime(path, entry), nil
	}
	entry, err = v.resolve(ctx, path)
	if err != nil {
		return drive.Entry{}, err
	}
	return v.applyLocalModTime(path, entry), nil
}

func uploadModTime(p PendingUpload) time.Time {
	if p.ModTime == 0 {
		if p.UpdatedAt == 0 {
			return time.Time{}
		}
		return time.Unix(0, p.UpdatedAt)
	}
	return time.Unix(0, p.ModTime)
}

func cloneEntries(entries []drive.Entry) []drive.Entry {
	if entries == nil {
		return nil
	}
	cloned := make([]drive.Entry, len(entries))
	copy(cloned, entries)
	return cloned
}

func cleanVirtual(path string) string {
	return CleanVirtualPath(path)
}

func isAppleMetadataName(name string) bool {
	return name == ".DS_Store" || strings.HasPrefix(name, "._")
}

func (v *VFS) FlushReadCache() error {
	return v.read.FlushReadCache()
}

func (v *VFS) ClearReadCache() error {
	return v.read.ClearReadCache()
}

func (v *VFS) ClearReadCacheForMount(name string) error {
	if name != "" && cleanMountName(name) != cleanMountName(v.name) {
		return fmt.Errorf("vfs: unknown mount %q", name)
	}
	return v.ClearReadCache()
}

func (v *VFS) CloseReadCache() error {
	return v.read.Close()
}

func (v *VFS) Resume(ctx context.Context) {
	for _, pending := range v.uploads.Store().PendingUploads() {
		if info, err := os.Stat(pending.LocalPath); err == nil && info.Size() != pending.Size {
			oldSize := pending.Size
			pending.Size = info.Size()
			pending.UpdatedAt = util.Now().UnixNano()
			if err := v.uploads.Store().SaveUpload(pending); err != nil {
				logging.L.Warnf("[VFS] repair pending staging size failed op_id=%q path=%q old_size=%d staging_size=%d err=%v", pending.FID, pending.Path, oldSize, pending.Size, err)
			} else {
				logging.L.InfofEvery("vfs.repair_pending_staging_size", time.Second, "[VFS] repaired pending staging size op_id=%q path=%q old_size=%d staging_size=%d", pending.FID, pending.Path, oldSize, pending.Size)
			}
		}
		if pending.PermanentFail {
			logging.L.WarnfEvery("vfs.resume_pending_permanent_failure", time.Second, "[VFS] skip permanently failed upload op_id=%q path=%q name=%q size=%d local=%q retry=%d last_error=%q", pending.FID, pending.Path, pending.Name, pending.Size, pending.LocalPath, pending.RetryCount, pending.LastError)
			continue
		}
		if !pending.Frozen {
			logging.L.InfofEvery("vfs.resume_pending_mutable", time.Second, "[VFS] keep unflushed pending local; waiting for next flush op_id=%q path=%q name=%q size=%d local=%q", pending.FID, pending.Path, pending.Name, pending.Size, pending.LocalPath)
			continue
		}
		logging.L.InfofEvery("vfs.resume_pending", time.Second, "[VFS] resume pending upload op_id=%q path=%q name=%q size=%d local=%q retry=%d last_error=%q", pending.FID, pending.Path, pending.Name, pending.Size, pending.LocalPath, pending.RetryCount, pending.LastError)
		v.enqueue(pending)
	}
}

func (v *VFS) recordHealthResult(op string, err error) {
	v.healthTracker.RecordResult(op, err)
}

func (v *VFS) Space(ctx context.Context) (drive.Space, error) {
	return newVFSDriverRuntime(v).Space(ctx)
}

func (v *VFS) invalidateReadCache(entry drive.Entry) {
	if entry.ID == "" {
		return
	}
	if cacheKey := v.readCacheKey(entry); cacheKey != "" {
		v.read.InvalidateFile(cacheKey)
	}
	v.read.InvalidateFile(entry.ID)
}

func (v *VFS) pendingUpload(path string) (PendingUpload, error) {
	path = cleanVirtual(path)
	if pending, ok := v.uploads.Store().UploadByPath(path); ok {
		return pending, nil
	}
	return PendingUpload{}, fmt.Errorf("vfs: no pending file for %s", path)
}

func (v *VFS) lockPath(path string) func() {
	return v.pathLocks.lock(cleanVirtual(path))
}
