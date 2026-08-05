// Package vfs provides the platform-independent file API used by CLI, FUSE,
// and future mobile adapters.
package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	readCache     *readCacheStore
	uploads       *uploadStore
	rootID        string
	encrypted     bool
	testEnabled   bool

	view        *viewState
	uploadQueue chan PendingUpload

	deleteTasks    *deleteTaskState
	uploadDelay    time.Duration
	uploadWorkers  int
	uploadSchedule *uploadScheduleState
	uploadDebug    *uploadDebugState
	uploadFaults   *uploadFaultState
	uploadHashes   *uploadHashTrackerState
	uploadAdmit    uploadAdmission
	readHistory    *readHistoryState
	activeDebug    *activeDebugState

	// done is closed when the VFS shuts down (context cancel in Start). The
	// blocking upload-queue enqueue goroutine selects on it so it cannot
	// leak after the upload workers have exited.
	done chan struct{}

	deleteDelay time.Duration

	readPrefetch *readPrefetchState

	readSlots *readSlotState

	dirPrefetch *dirPrefetchState

	listState *listState

	readFastPath *readFastPathState

	readWindows *readWindowState

	pathLocks *pathLockState
}

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
	stores, err := NewStores(uploadDir, readCacheDir, opts.CacheMaxBytes)
	if err != nil {
		return nil, err
	}
	now := timeutil.Now()
	overlay, deleteTasks := newDeleteStates()
	view := newViewState(opts.RootID, now)
	view.overlay = overlay
	v := &VFS{
		driver:         driver,
		name:           opts.Name,
		healthTracker:  drive.NewHealthTracker(drive.DefaultHealthWindow, drive.DefaultMaxEvents),
		readCache:      stores.readCacheStore,
		uploads:        stores.uploadStore,
		rootID:         opts.RootID,
		encrypted:      opts.Encrypted,
		testEnabled:    opts.TestEnabled,
		done:           make(chan struct{}),
		view:           view,
		uploadQueue:    make(chan PendingUpload, 128),
		deleteTasks:    deleteTasks,
		uploadDelay:    opts.UploadDelay,
		uploadWorkers:  opts.UploadWorkers,
		uploadSchedule: newUploadScheduleState(),
		uploadDebug:    newUploadDebugState(),
		uploadFaults:   newUploadFaultState(),
		uploadHashes:   newUploadHashTrackerState(),
		readHistory:    newReadHistoryState(),
		activeDebug:    newActiveDebugState(),
		deleteDelay:    opts.DeleteDelay,
		readPrefetch:   newReadPrefetchState(),
		readSlots:      newReadSlotState(),
		dirPrefetch:    newDirPrefetchState(),
		listState:      newListState(),
		readFastPath:   newReadFastPathState(),
		readWindows:    newReadWindowState(),
		pathLocks:      newPathLockState(),
	}
	return v, nil
}

func (v *VFS) Start(ctx context.Context) {
	for i := 0; i < v.uploadWorkers; i++ {
		go v.uploadWorker(ctx)
	}
	v.Resume(ctx)
	// Graceful stop: when the context is cancelled, stop the upload/delete
	// timers and close the read-cache writer so no background goroutine
	// outlives the VFS. Upload workers exit on ctx.Done themselves; the
	// cache writer only exits when its queue is closed, which CloseReadCache
	// does and waits for.
	context.AfterFunc(ctx, func() {
		close(v.done)
		v.stopDeleteTimers()
		v.stopUploadTimers()
		_ = v.CloseReadCache()
	})
}

func (v *VFS) StartDirectoryPrefetch(ctx context.Context) {
	if !newVFSListScheduler(v).StartDirPrefetch(ctx) {
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
	return v.readCache.FlushReadCache()
}

func (v *VFS) ClearReadCache() error {
	return v.readCache.ClearReadCache()
}

func (v *VFS) CloseReadCache() error {
	return v.readCache.Close()
}

func (v *VFS) Resume(ctx context.Context) {
	for _, pending := range v.uploads.PendingUploads() {
		if info, err := os.Stat(pending.LocalPath); err == nil && info.Size() != pending.Size {
			oldSize := pending.Size
			pending.Size = info.Size()
			pending.UpdatedAt = timeutil.Now().UnixNano()
			if err := v.uploads.SaveUpload(pending); err != nil {
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
		v.readCache.InvalidateFile(cacheKey)
	}
	v.readCache.InvalidateFile(entry.ID)
}

func (v *VFS) pendingUpload(path string) (PendingUpload, error) {
	path = cleanVirtual(path)
	if pending, ok := v.uploads.UploadByPath(path); ok {
		return pending, nil
	}
	return PendingUpload{}, fmt.Errorf("vfs: no pending file for %s", path)
}

func (v *VFS) lockPath(path string) func() {
	path = cleanVirtual(path)
	v.pathLocks.mu.Lock()
	mu := v.pathLocks.locks[path]
	if mu == nil {
		mu = &sync.Mutex{}
		v.pathLocks.locks[path] = mu
	}
	v.pathLocks.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}
