// Package vfs provides the platform-independent file API used by CLI, FUSE,
// and future mobile adapters.
package vfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
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
	CacheDir      string
	CacheMaxBytes int64
	RootID        string
	UploadDelay   time.Duration
	UploadWorkers int
	DeleteDelay   time.Duration
}

type VFS struct {
	driver        drive.Driver
	writer        drive.Writer
	sourceUpload  drive.SourceUploader
	name          string
	healthTracker *drive.HealthTracker
	cache         *Cache
	rootID        string

	mu      sync.RWMutex
	entries map[string]drive.Entry
	lists   map[string]listCacheEntry
	queue   chan PendingFile

	uploadDelay   time.Duration
	uploadWorkers int
	uploadMu      sync.Mutex
	uploadTimers  map[string]*time.Timer
	activeUploads map[string]*debugUploadState
	uploadHistory []DebugUpload

	deleteDelay  time.Duration
	deleteMu     sync.Mutex
	deleteTimers map[string]*time.Timer
	deleted      map[string]drive.Entry
	overlayOps   map[string]overlayOp
	restoredDirs map[string]time.Time
	copyHidden   map[string]map[string]time.Time
	localDirs    map[string]time.Time
	localModTime map[string]time.Time

	prefetchMu  sync.Mutex
	prefetching map[string]struct{}
	prefetchSem chan struct{}

	dirPrefetchMu      sync.Mutex
	dirPrefetching     map[string]struct{}
	dirPrefetched      map[string]time.Time
	dirPrefetchSem     chan struct{}
	dirPrefetchContext context.Context
	dirPrefetchStarted bool

	listLoadMu sync.Mutex
	listLoads  map[string]*listLoad

	chunkLoadMu sync.Mutex
	chunkLoads  map[string]*chunkLoad

	windowLoadMu sync.Mutex
	windowLoads  map[string]*windowLoad

	pathLockMu sync.Mutex
	pathLocks  map[string]*sync.Mutex
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
	// CacheDir is scoped to the current mount's driver/encryption mode.
	// If a mount is switched between plain and crypt, stop qrypt and clear
	// that mount's cache directory first; pending journal entries, staging
	// files, and read-cache chunks all carry IDs/names with the old semantics.
	cache, err := NewCache(opts.CacheDir, opts.CacheMaxBytes)
	if err != nil {
		return nil, err
	}
	v := &VFS{
		driver:         driver,
		name:           opts.Name,
		healthTracker:  drive.NewHealthTracker(drive.DefaultHealthWindow, drive.DefaultMaxEvents),
		cache:          cache,
		rootID:         opts.RootID,
		entries:        map[string]drive.Entry{},
		lists:          map[string]listCacheEntry{},
		queue:          make(chan PendingFile, 128),
		uploadDelay:    opts.UploadDelay,
		uploadWorkers:  opts.UploadWorkers,
		uploadTimers:   map[string]*time.Timer{},
		activeUploads:  map[string]*debugUploadState{},
		deleteDelay:    opts.DeleteDelay,
		deleteTimers:   map[string]*time.Timer{},
		deleted:        map[string]drive.Entry{},
		overlayOps:     map[string]overlayOp{},
		restoredDirs:   map[string]time.Time{},
		copyHidden:     map[string]map[string]time.Time{},
		localDirs:      map[string]time.Time{},
		localModTime:   map[string]time.Time{},
		prefetching:    map[string]struct{}{},
		prefetchSem:    make(chan struct{}, readPrefetchLimit),
		dirPrefetching: map[string]struct{}{},
		dirPrefetched:  map[string]time.Time{},
		dirPrefetchSem: make(chan struct{}, dirPrefetchLimit),
		listLoads:      map[string]*listLoad{},
		chunkLoads:     map[string]*chunkLoad{},
		windowLoads:    map[string]*windowLoad{},
		pathLocks:      map[string]*sync.Mutex{},
	}
	if drive.HasCapability(driver, drive.CapabilityWriter) {
		v.writer, _ = driver.(drive.Writer)
	}
	if drive.HasCapability(driver, drive.CapabilitySourceUploader) {
		v.sourceUpload, _ = driver.(drive.SourceUploader)
	}
	v.entries["/"] = drive.Entry{ID: opts.RootID, Name: "/", IsDir: true, ModTime: timeutil.Now()}
	return v, nil
}

func (v *VFS) Start(ctx context.Context) {
	for i := 0; i < v.uploadWorkers; i++ {
		go v.uploadWorker(ctx)
	}
	v.Resume(ctx)
}

func (v *VFS) StartDirectoryPrefetch(ctx context.Context) {
	v.dirPrefetchMu.Lock()
	if v.dirPrefetchStarted {
		v.dirPrefetchMu.Unlock()
		return
	}
	v.dirPrefetchStarted = true
	v.dirPrefetchContext = ctx
	v.dirPrefetchMu.Unlock()

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

func (v *VFS) Resume(ctx context.Context) {
	for _, pending := range v.cache.Pending() {
		logging.L.InfofEvery("vfs.resume_pending", time.Second, "[VFS] resume pending upload op_id=%q path=%q name=%q size=%d local=%q retry=%d last_error=%q", pending.FID, pending.Path, pending.Name, pending.Size, pending.LocalPath, pending.RetryCount, pending.LastError)
		v.enqueue(pending)
	}
}

func (v *VFS) recordHealthResult(op string, err error) {
	v.healthTracker.RecordResult(op, err)
}

func (v *VFS) Space(ctx context.Context) (space drive.Space, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpSpace, err) }()
	if !drive.HasCapability(v.driver, drive.CapabilitySpace) {
		return drive.Space{}, fmt.Errorf("vfs: driver does not support space query")
	}
	querier := v.driver.(drive.SpaceQuerier)
	return querier.Space(ctx)
}

func (v *VFS) Stat(ctx context.Context, path string) (entry drive.Entry, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpStat, err) }()
	path = cleanVirtual(path)
	if pending, err := v.pending(path); err == nil {
		entry := drive.Entry{
			ID:       pending.FID,
			ParentID: pending.ParentID,
			Name:     pending.Name,
			IsDir:    false,
			Size:     pending.Size,
			ModTime:  pendingModTime(pending),
		}
		return v.applyLocalModTime(path, entry), nil
	}
	entry, err = v.resolve(ctx, path)
	if err != nil {
		return drive.Entry{}, err
	}
	return v.applyLocalModTime(path, entry), nil
}

func (v *VFS) List(ctx context.Context, path string) ([]drive.Entry, error) {
	entries, err := v.listNoPrefetch(ctx, path)
	v.healthTracker.RecordResult(drive.HealthOpList, err)
	if err != nil {
		return nil, err
	}
	v.scheduleDirPrefetch(ctx, cleanVirtual(path), entries)
	return entries, nil
}

func (v *VFS) listNoPrefetch(ctx context.Context, path string) ([]drive.Entry, error) {
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	entries, err := v.listChildren(ctx, path, entry.ID)
	if err != nil {
		return nil, err
	}
	entries = v.withPendingChildren(path, entries)
	return entries, nil
}

func (v *VFS) RemoteList(ctx context.Context, path string) ([]drive.Entry, error) {
	path = cleanVirtual(path)
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	if !entry.IsDir {
		return nil, fmt.Errorf("vfs: %s is not a directory", path)
	}
	entries, err := v.driver.List(ctx, entry.ID)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

func (v *VFS) Read(ctx context.Context, path string, offset, size int64) (rc io.ReadCloser, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpRead, err) }()
	if pending, err := v.pending(path); err == nil {
		if err := v.cache.staging.flush(pending.LocalPath); err != nil {
			return nil, err
		}
		return osutil.OpenRead(pending.LocalPath, offset, size)
	}
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	if entry.IsDir {
		return nil, fmt.Errorf("vfs: %s is a directory", path)
	}
	data, startChunk, endChunk, err := v.readRange(ctx, entry, offset, size)
	if err != nil {
		return nil, err
	}
	v.prefetchAdjacentChunks(ctx, entry, startChunk, endChunk)
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (v *VFS) Create(ctx context.Context, path string) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpCreate, err) }()
	if v.sourceUpload == nil {
		return fmt.Errorf("vfs: driver does not support upload")
	}
	path = cleanVirtual(path)
	unlock := v.lockPath(path)
	defer unlock()
	return v.createLocked(ctx, path)
}

func (v *VFS) createLocked(ctx context.Context, path string) error {
	path = cleanVirtual(path)
	v.restoreDeletedAncestor(filepath.Dir(path))
	v.cancelDeletedFile(path)
	parent, name, err := v.parent(ctx, path)
	if err != nil {
		return err
	}
	v.unhideCopyChild(filepath.Dir(path), name)
	fid := stagingFID(path)
	localPath, err := v.cache.staging.create(fid)
	if err != nil {
		return err
	}
	now := timeutil.Now()
	pending := PendingFile{Path: path, FID: fid, ParentID: parent.ID, Name: name, LocalPath: localPath, ModTime: now.UnixNano()}
	if err := v.cache.SavePending(pending); err != nil {
		return err
	}
	v.setLocalModTime(path, now)
	logging.L.InfofEvery("vfs.pending_created", time.Second, "[VFS] pending created op_id=%q path=%q parent=%q name=%q local=%q", pending.FID, path, parent.ID, name, localPath)
	return nil
}

func (v *VFS) WriteAt(ctx context.Context, path string, data []byte, off int64) (n int, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpWrite, err) }()
	path = cleanVirtual(path)
	unlock := v.lockPath(path)
	defer unlock()
	pending, err := v.pending(path)
	if err != nil {
		if entry, resolveErr := v.resolve(ctx, path); resolveErr == nil && !entry.IsDir {
			v.invalidateReadCache(entry)
			logging.L.InfofEvery("vfs.stage_existing_for_write", time.Second, "[VFS] staging existing file for write path=%q id=%q size=%d", path, entry.ID, entry.Size)
			if err := v.stageExisting(ctx, path); err != nil {
				return 0, err
			}
		} else {
			if err := v.createLocked(ctx, path); err != nil {
				return 0, err
			}
		}
		pending, err = v.pending(path)
		if err != nil {
			return 0, err
		}
	}
	n, err = v.cache.staging.writeAt(pending.LocalPath, data, off)
	if err != nil {
		return n, err
	}
	size, _ := v.cache.staging.size(pending.LocalPath)
	pending.Size = size
	now := timeutil.Now()
	pending.ModTime = now.UnixNano()
	if err := v.cache.SavePending(pending); err != nil {
		return n, err
	}
	v.setLocalModTime(path, now)
	logging.L.DebugfEvery("vfs.write_staged", time.Second, "[VFS] write staged op_id=%q path=%q off=%d len=%d written=%d size=%d local=%q", pending.FID, path, off, len(data), n, pending.Size, pending.LocalPath)
	return n, nil
}

func (v *VFS) Flush(ctx context.Context, path string) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpWrite, err) }()
	path = cleanVirtual(path)
	unlock := v.lockPath(path)
	defer unlock()
	pending, err := v.pending(path)
	if err != nil {
		logging.L.DebugfEvery("vfs.flush_ignored", time.Second, "[VFS] flush ignored without pending path=%q", path)
		return nil
	}
	if err := v.cache.staging.flush(pending.LocalPath); err != nil {
		return err
	}
	if err := v.cache.staging.sync(pending.LocalPath); err != nil {
		return err
	}
	size, err := v.cache.staging.size(pending.LocalPath)
	if err != nil {
		return err
	}
	pending.Size = size
	if pending.ModTime == 0 {
		now := timeutil.Now()
		pending.ModTime = now.UnixNano()
		v.setLocalModTime(path, now)
	}
	if err := v.cache.SavePending(pending); err != nil {
		return err
	}
	if latest, ok := v.cache.PendingByPath(path); ok {
		pending = latest
	}
	delay := v.uploadDelay
	if pending.Size == 0 && delay < zeroByteUploadDebounceDelay {
		delay = zeroByteUploadDebounceDelay
	}
	logging.L.InfofEvery("vfs.flush_queued", time.Second, "[VFS] flush queued upload op_id=%q path=%q name=%q size=%d local=%q delay=%s", pending.FID, pending.Path, pending.Name, pending.Size, pending.LocalPath, delay)
	v.enqueueAfter(pending, delay)
	return nil
}

func (v *VFS) PrepareDirectoryCopy(ctx context.Context, path string) error {
	path = cleanVirtual(path)
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	if !entry.IsDir {
		return fmt.Errorf("vfs: %s is not a directory", path)
	}
	hideNames := map[string]time.Time{}
	if entries, err := v.driver.List(ctx, entry.ID); err == nil {
		expires := time.Now().Add(directoryCopyHideTTL)
		for _, child := range entries {
			if !isAppleMetadataName(child.Name) {
				hideNames[child.Name] = expires
			}
		}
	}
	v.cancelChildUploads(path)
	if err := v.cache.RemovePendingUnder(path); err != nil {
		return err
	}
	v.mu.Lock()
	for cachedPath, cachedEntry := range v.entries {
		if filepath.Dir(cachedPath) == path {
			if _, ok := hideNames[cachedEntry.Name]; !ok && !isAppleMetadataName(cachedEntry.Name) {
				hideNames[cachedEntry.Name] = time.Now().Add(directoryCopyHideTTL)
			}
			delete(v.entries, cachedPath)
		}
	}
	v.markLocalDirLocked(path)
	v.invalidateListLocked(path)
	v.mu.Unlock()
	v.setCopyHidden(path, hideNames)
	return nil
}

func (v *VFS) withPendingChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	parentPath = cleanVirtual(parentPath)
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.Name] = true
	}
	for _, pending := range v.cache.Pending() {
		if filepath.Dir(pending.Path) != parentPath || seen[pending.Name] || v.isDeleted(pending.Path) {
			continue
		}
		entries = append(entries, drive.Entry{
			ID:       pending.FID,
			ParentID: pending.ParentID,
			Name:     pending.Name,
			Size:     pending.Size,
			ModTime:  pendingModTime(pending),
		})
		seen[pending.Name] = true
	}
	return entries
}

func (v *VFS) Mkdir(ctx context.Context, path string) (entry drive.Entry, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpMkdir, err) }()
	if v.writer == nil {
		return drive.Entry{}, fmt.Errorf("vfs: driver does not support mkdir")
	}
	path = cleanVirtual(path)
	if entry, err := v.resolve(ctx, path); err == nil {
		if entry.IsDir {
			logging.L.Debugf("[VFS] mkdir skipped existing directory path=%q id=%q", path, entry.ID)
			return entry, nil
		}
		return drive.Entry{}, fmt.Errorf("vfs: %s exists and is not a directory", path)
	}
	if entry, ok := v.restoreDeletedPath(path); ok {
		logging.L.InfofEvery("vfs.mkdir_restored_deleted", time.Second, "[VFS] mkdir restored deleted directory path=%q id=%q", path, entry.ID)
		return entry, nil
	}
	v.restoreDeletedAncestor(filepath.Dir(path))
	if v.isUnderRestoredDir(path) {
		if entry, err := v.resolve(ctx, path); err == nil && entry.IsDir {
			logging.L.InfofEvery("vfs.mkdir_reused_restored", time.Second, "[VFS] mkdir reused restored ancestor path=%q id=%q", path, entry.ID)
			return entry, nil
		}
	}
	parent, name, err := v.parent(ctx, path)
	if err != nil {
		logging.L.Warnf("[VFS] mkdir parent resolve failed path=%q err=%v", path, err)
		return drive.Entry{}, err
	}
	logging.L.InfofEvery("vfs.mkdir_start", time.Second, "[VFS] mkdir start path=%q parent=%q name=%q", path, parent.ID, name)
	entry, err = v.writer.Mkdir(ctx, parent.ID, name)
	if err != nil {
		if !isAlreadyExistsError(err) {
			logging.L.Warnf("[VFS] mkdir failed path=%q parent=%q name=%q err=%v", path, parent.ID, name, err)
			return drive.Entry{}, err
		}
		logging.L.InfofEvery("vfs.mkdir_already_exists", time.Second, "[VFS] mkdir already exists; resolving existing directory path=%q parent=%q name=%q", path, parent.ID, name)
		entry, err = v.findExistingChildDir(ctx, filepath.Dir(path), parent.ID, name)
		if err != nil {
			logging.L.Warnf("[VFS] mkdir existing directory resolve failed path=%q parent=%q name=%q err=%v", path, parent.ID, name, err)
			return drive.Entry{}, err
		}
	}
	v.mu.Lock()
	v.entries[path] = entry
	v.markLocalDirLocked(path)
	v.invalidateListLocked(filepath.Dir(path))
	v.mu.Unlock()
	logging.L.InfofEvery("vfs.mkdir_complete", time.Second, "[VFS] mkdir complete path=%q id=%q parent=%q", path, entry.ID, entry.ParentID)
	return entry, nil
}

func (v *VFS) findExistingChildDir(ctx context.Context, parentPath, parentID, name string) (drive.Entry, error) {
	entries, err := v.driver.List(ctx, parentID)
	if err != nil {
		return drive.Entry{}, err
	}
	v.mu.Lock()
	for _, child := range entries {
		childPath := joinVirtual(parentPath, child.Name)
		v.entries[childPath] = child
		if child.Name == name && child.IsDir {
			v.mu.Unlock()
			return child, nil
		}
	}
	v.mu.Unlock()
	return drive.Entry{}, fmt.Errorf("vfs: existing directory not found: %s", joinVirtual(parentPath, name))
}

func (v *VFS) Remove(ctx context.Context, path string) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpDelete, err) }()
	if v.writer == nil {
		return fmt.Errorf("vfs: driver does not support remove")
	}
	path = cleanVirtual(path)
	if _, err := v.pending(path); err == nil {
		unlock := v.lockPath(path)
		defer unlock()
		v.cancelUpload(path)
		v.clearLocalModTime(path)
		return v.cache.RemovePending(path)
	}
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	v.invalidateReadCache(entry)
	v.markDeleted(path, entry)
	v.clearLocalModTime(path)
	logging.L.Infof("[VFS] remove queued path=%q id=%q dir=%t delay=%s", path, entry.ID, entry.IsDir, v.deleteDelay)
	v.scheduleDelete(path, entry)
	return nil
}

func (v *VFS) RemoveDir(ctx context.Context, path string) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpDelete, err) }()
	if v.writer == nil {
		return fmt.Errorf("vfs: driver does not support remove")
	}
	path = cleanVirtual(path)
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	if !entry.IsDir {
		return fmt.Errorf("vfs: %s is not a directory", path)
	}
	v.invalidateReadCache(entry)
	v.cancelChildUploads(path)
	if err := v.cache.RemovePendingUnder(path); err != nil {
		return err
	}
	v.cancelChildDeletes(path)
	v.markDeleted(path, entry)
	v.clearLocalModTime(path)
	logging.L.Infof("[VFS] remove dir queued path=%q id=%q delay=%s", path, entry.ID, v.deleteDelay)
	v.scheduleDelete(path, entry)
	return nil
}

func (v *VFS) Rename(ctx context.Context, oldPath, newPath string) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpRename, err) }()
	if v.writer == nil {
		return fmt.Errorf("vfs: driver does not support rename")
	}
	oldPath = cleanVirtual(oldPath)
	newPath = cleanVirtual(newPath)
	if oldPath == "/" || newPath == "/" {
		return fmt.Errorf("vfs: cannot rename root")
	}

	if pending, err := v.pending(oldPath); err == nil {
		unlockOld := v.lockPath(oldPath)
		defer unlockOld()
		parent, name, err := v.parent(ctx, newPath)
		if err != nil {
			return err
		}
		pending.Path = newPath
		pending.ParentID = parent.ID
		pending.Name = name
		v.moveLocalModTime(oldPath, newPath)
		return v.cache.RenamePending(oldPath, pending)
	}

	entry, err := v.resolve(ctx, oldPath)
	if err != nil {
		return err
	}
	v.invalidateReadCache(entry)
	dstParent, newName, err := v.parent(ctx, newPath)
	if err != nil {
		return err
	}
	oldParent := filepath.Dir(oldPath)
	newParent := filepath.Dir(newPath)
	if filepath.Base(oldPath) != newName {
		if err := v.writer.Rename(ctx, entry, newName); err != nil {
			return err
		}
		entry.Name = newName
	}
	if oldParent != newParent {
		if err := v.writer.Move(ctx, entry, dstParent.ID); err != nil {
			return err
		}
		entry.ParentID = dstParent.ID
	}
	v.mu.Lock()
	delete(v.entries, oldPath)
	delete(v.entries, newPath)
	v.rebaseCachedPathsLocked(oldPath, newPath)
	v.moveLocalModTimeLocked(oldPath, newPath)
	v.invalidateListLocked(oldParent)
	v.invalidateListLocked(newParent)
	entry.Name = newName
	entry.ParentID = dstParent.ID
	entry = v.applyLocalModTimeLocked(newPath, entry)
	v.entries[newPath] = entry
	v.mu.Unlock()
	v.addOverlay(oldPath, newPath, entry.ID, entry.IsDir)
	return nil
}

func (v *VFS) Truncate(ctx context.Context, path string, size int64) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpWrite, err) }()
	if size < 0 {
		return fmt.Errorf("vfs: truncate size must be non-negative")
	}
	path = cleanVirtual(path)
	unlock := v.lockPath(path)
	defer unlock()
	pending, err := v.pending(path)
	if err != nil {
		if err := v.stageExisting(ctx, path); err != nil {
			return err
		}
		pending, err = v.pending(path)
		if err != nil {
			return err
		}
	}
	if err := v.cache.staging.truncate(pending.LocalPath, size); err != nil {
		return err
	}
	pending.Size = size
	now := timeutil.Now()
	pending.ModTime = now.UnixNano()
	v.setLocalModTime(path, now)
	if entry, err := v.resolve(ctx, path); err == nil && !entry.IsDir {
		v.invalidateReadCache(entry)
	}
	return v.cache.SavePending(pending)
}

func (v *VFS) stageExisting(ctx context.Context, path string) error {
	parent, name, err := v.parent(ctx, path)
	if err != nil {
		return err
	}
	fid := stagingFID(path)
	localPath, err := v.cache.staging.create(fid)
	if err != nil {
		return err
	}
	if entry, err := v.resolve(ctx, path); err == nil && !entry.IsDir {
		rc, err := v.driver.Read(ctx, entry, 0, 0)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(f, rc)
		closeErr := f.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	size, _ := v.cache.staging.size(localPath)
	modTime := timeutil.Now()
	if entry, err := v.resolve(ctx, path); err == nil && !entry.ModTime.IsZero() {
		modTime = entry.ModTime
	}
	pending := PendingFile{
		Path:      path,
		FID:       fid,
		ParentID:  parent.ID,
		Name:      name,
		LocalPath: localPath,
		Size:      size,
		ModTime:   modTime.UnixNano(),
	}
	if err := v.cache.SavePending(pending); err != nil {
		return err
	}
	logging.L.InfofEvery("vfs.existing_file_staged", time.Second, "[VFS] existing file staged op_id=%q path=%q parent=%q name=%q size=%d local=%q", pending.FID, path, parent.ID, name, size, localPath)
	return nil
}

func (v *VFS) lockPath(path string) func() {
	path = cleanVirtual(path)
	v.pathLockMu.Lock()
	mu := v.pathLocks[path]
	if mu == nil {
		mu = &sync.Mutex{}
		v.pathLocks[path] = mu
	}
	v.pathLockMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func (v *VFS) invalidateReadCache(entry drive.Entry) {
	if entry.ID == "" {
		return
	}
	v.cache.InvalidateFile(entry.ID)
}

func (v *VFS) pending(path string) (PendingFile, error) {
	path = cleanVirtual(path)
	for _, p := range v.cache.Pending() {
		if p.Path == path {
			return p, nil
		}
	}
	return PendingFile{}, fmt.Errorf("vfs: no pending file for %s", path)
}

func pendingModTime(p PendingFile) time.Time {
	if p.ModTime == 0 {
		if p.UpdatedAt == 0 {
			return time.Time{}
		}
		return time.Unix(0, p.UpdatedAt)
	}
	return time.Unix(0, p.ModTime)
}

func (v *VFS) SetModTime(ctx context.Context, path string, modTime time.Time) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpWrite, err) }()
	path = cleanVirtual(path)
	if modTime.IsZero() {
		return nil
	}
	unlock := v.lockPath(path)
	defer unlock()
	if _, err := v.pending(path); err == nil {
		v.setLocalModTime(path, modTime)
		return nil
	}
	if entry, err := v.resolve(ctx, path); err != nil {
		return err
	} else {
		entry.ModTime = modTime
		v.mu.Lock()
		v.entries[path] = entry
		v.setLocalModTimeLocked(path, modTime)
		v.invalidateListLocked(filepath.Dir(path))
		v.mu.Unlock()
	}
	return nil
}

func (v *VFS) applyLocalModTime(path string, entry drive.Entry) drive.Entry {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.applyLocalModTimeLocked(path, entry)
}

func (v *VFS) applyLocalModTimes(parentPath string, entries []drive.Entry) []drive.Entry {
	parentPath = cleanVirtual(parentPath)
	v.mu.RLock()
	defer v.mu.RUnlock()
	for i, entry := range entries {
		entries[i] = v.applyLocalModTimeLocked(joinVirtual(parentPath, entry.Name), entry)
	}
	return entries
}

func (v *VFS) applyLocalModTimeLocked(path string, entry drive.Entry) drive.Entry {
	if modTime, ok := v.localModTime[cleanVirtual(path)]; ok && !modTime.IsZero() {
		entry.ModTime = modTime
	}
	return entry
}

func (v *VFS) localModTimeFor(path string) time.Time {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.localModTime[cleanVirtual(path)]
}

func (v *VFS) setLocalModTime(path string, modTime time.Time) {
	v.mu.Lock()
	v.setLocalModTimeLocked(path, modTime)
	v.mu.Unlock()
}

func (v *VFS) setLocalModTimeLocked(path string, modTime time.Time) {
	if modTime.IsZero() {
		return
	}
	v.localModTime[cleanVirtual(path)] = modTime
}

func (v *VFS) clearLocalModTime(path string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	path = cleanVirtual(path)
	for knownPath := range v.localModTime {
		if knownPath == path || isPathUnder(knownPath, path) {
			delete(v.localModTime, knownPath)
		}
	}
}

func (v *VFS) moveLocalModTime(oldPath, newPath string) {
	v.mu.Lock()
	v.moveLocalModTimeLocked(oldPath, newPath)
	v.mu.Unlock()
}

func (v *VFS) moveLocalModTimeLocked(oldPath, newPath string) {
	oldPath = cleanVirtual(oldPath)
	newPath = cleanVirtual(newPath)
	for knownPath, modTime := range v.localModTime {
		if knownPath == oldPath {
			delete(v.localModTime, knownPath)
			v.localModTime[newPath] = modTime
			continue
		}
		if isPathUnder(knownPath, oldPath) {
			nextPath := joinVirtual(newPath, strings.TrimPrefix(knownPath, oldPath+"/"))
			delete(v.localModTime, knownPath)
			v.localModTime[nextPath] = modTime
		}
	}
}

func (v *VFS) rebaseCachedPathsLocked(oldPath, newPath string) {
	oldPath = cleanVirtual(oldPath)
	newPath = cleanVirtual(newPath)
	for path, entry := range v.entries {
		if !isPathUnder(path, oldPath) {
			continue
		}
		nextPath := joinVirtual(newPath, strings.TrimPrefix(path, oldPath+"/"))
		delete(v.entries, path)
		v.entries[nextPath] = entry
	}
}

func (v *VFS) parent(ctx context.Context, path string) (drive.Entry, string, error) {
	path = cleanVirtual(path)
	name, parentPath := splitVirtual(path)
	parent, err := v.resolve(ctx, parentPath)
	return parent, name, err
}

func (v *VFS) resolve(ctx context.Context, path string) (drive.Entry, error) {
	path = cleanVirtual(path)
	if v.isUnavailable(path) {
		return drive.Entry{}, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	v.mu.RLock()
	entry, ok := v.entries[path]
	v.mu.RUnlock()
	if ok {
		return entry, nil
	}
	name, parentPath := splitVirtual(path)
	parent, err := v.resolve(ctx, parentPath)
	if err != nil {
		return drive.Entry{}, err
	}
	if v.isRecentLocalDir(parentPath) {
		return drive.Entry{}, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	entries, err := v.listChildren(ctx, parentPath, parent.ID)
	if err != nil {
		return drive.Entry{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, child := range entries {
		childPath := joinVirtual(parentPath, child.Name)
		child = v.applyLocalModTimeLocked(childPath, child)
		v.entries[childPath] = child
		if child.Name == name {
			return child, nil
		}
	}
	return drive.Entry{}, fmt.Errorf("%w: %s", ErrNotFound, path)
}

func (v *VFS) markLocalDirLocked(path string) {
	v.localDirs[cleanVirtual(path)] = time.Now().Add(localCreateLookupTTL)
}

func (v *VFS) isRecentLocalDir(path string) bool {
	path = cleanVirtual(path)
	now := time.Now()
	v.mu.Lock()
	defer v.mu.Unlock()
	expires, ok := v.localDirs[path]
	if !ok {
		return false
	}
	if now.After(expires) {
		delete(v.localDirs, path)
		return false
	}
	return true
}

func (v *VFS) invalidateListLocked(path string) {
	delete(v.lists, cleanVirtual(path))
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
	path = filepath.ToSlash(filepath.Clean("/" + strings.TrimPrefix(path, "/")))
	if path == "." {
		return "/"
	}
	return path
}

// splitVirtual splits a cleaned virtual path into the last component (name)
// and its parent directory. Unlike filepath.Dir/Base, this uses forward-slash
// semantics regardless of the host OS, which is required for virtual FUSE paths.
func splitVirtual(p string) (name, parent string) {
	if p == "/" {
		return "/", "/"
	}
	idx := strings.LastIndexByte(p, '/')
	if idx <= 0 {
		return p[1:], "/"
	}
	return p[idx+1:], p[:idx]
}

func joinVirtual(parent, name string) string {
	parent = cleanVirtual(parent)
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

func isPathUnder(path, dir string) bool {
	path = cleanVirtual(path)
	dir = cleanVirtual(dir)
	return dir != "/" && strings.HasPrefix(path, dir+"/")
}

func isAppleMetadataFile(path string) bool {
	return isAppleMetadataName(filepath.Base(cleanVirtual(path)))
}

func isAppleMetadataName(name string) bool {
	return name == ".DS_Store" || strings.HasPrefix(name, "._")
}

func isAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "already exists") ||
		strings.Contains(text, "file exists") ||
		strings.Contains(text, "同名冲突") ||
		strings.Contains(text, "已存在")
}

func entryListHasPath(entries []drive.Entry, name, entryID string) bool {
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}
		if entryID == "" || entry.ID == "" || entry.ID == entryID {
			return true
		}
	}
	return false
}

func stagingFID(path string) string {
	path = strings.Trim(cleanVirtual(path), "/")
	if path == "" {
		return "root"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_")
	return replacer.Replace(path)
}
