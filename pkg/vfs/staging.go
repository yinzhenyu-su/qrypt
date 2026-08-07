package vfs

import (
	"context"
	"errors"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type stagingStore struct {
	dir   string
	pages sync.Map
}

type page struct {
	mu        sync.Mutex
	fid       string
	buf       []byte
	dirty     bool
	maxOffset int64
	timer     *time.Timer
	flush     func(string, []byte) error
}

func newStagingStore(dir string) (*stagingStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &stagingStore{dir: dir}, nil
}

func (s *stagingStore) cleanupUploadTemps() int {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0
	}
	var cleaned int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.Contains(name, ".staging.upload-") {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err == nil {
			cleaned++
		}
	}
	return cleaned
}

func (s *stagingStore) path(fid string) string {
	return filepath.Join(s.dir, fid+".staging")
}

func (s *stagingStore) create(fid string) (string, error) {
	path := s.path(fid)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}

func (s *stagingStore) writeAt(path string, data []byte, off int64) (int, error) {
	if err := s.flush(path); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.WriteAt(data, off)
}

func (s *stagingStore) size(path string) (int64, error) {
	if err := s.flush(path); err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s *stagingStore) truncate(path string, size int64) error {
	if err := s.flush(path); err != nil {
		return err
	}
	s.pages.Delete(fidFromStagingPath(path))
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := s.create(fidFromStagingPath(path)); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := os.Truncate(path, size); err != nil {
		return err
	}
	return s.sync(path)
}

func (s *stagingStore) remove(path string) error {
	s.pages.Delete(fidFromStagingPath(path))
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *stagingStore) flush(path string) error {
	if p, ok := s.pages.Load(fidFromStagingPath(path)); ok {
		return p.(*page).flushNow()
	}
	return nil
}

func (s *stagingStore) sync(path string) error {
	if err := s.flush(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (p *page) flushNow() error {
	p.mu.Lock()
	if !p.dirty {
		p.mu.Unlock()
		return nil
	}
	data := make([]byte, p.maxOffset)
	copy(data, p.buf[:p.maxOffset])
	p.dirty = false
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.mu.Unlock()
	return p.flush(p.fid, data)
}

func fidFromStagingPath(path string) string {
	base := filepath.Base(path)
	if filepath.Ext(base) == ".staging" {
		return base[:len(base)-len(".staging")]
	}
	return base
}

// --- staging_write.go ---

func (v *VFS) Create(ctx context.Context, path string) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpCreate, err) }()
	if err := newVFSDriverRuntime(v).RequireCapability(drive.CapabilitySourceUploader, "upload"); err != nil {
		return err
	}
	path = cleanVirtual(path)
	unlock := v.lockPath(path)
	defer unlock()
	return v.createLocked(ctx, path)
}

func stagingFID(path string) string {
	path = strings.Trim(cleanVirtual(path), "/")
	if path == "" {
		return "root"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_")
	return replacer.Replace(path)
}

func newStagingFID(path string) string {
	base := stagingFID(path)
	return base + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func (v *VFS) createLocked(ctx context.Context, path string) error {
	runtime := newVFSUploadWriteRuntime(v)
	return v.createLockedWithStore(ctx, path, runtime.Store(), runtime.HashTracker())
}

func (v *VFS) createLockedWithStore(ctx context.Context, path string, store uploadStoreWriteAdapter, hashes vfsUploadWriteHashTracker) error {
	path = cleanVirtual(path)
	v.restoreDeletedAncestor(filepath.Dir(path))
	v.cancelDeletedFile(path)
	parent, name, err := v.parent(ctx, path)
	if err != nil {
		return err
	}
	v.unhideCopyChild(filepath.Dir(path), name)
	old, hadOld := store.UploadByPath(path)
	fid := newStagingFID(path)
	localPath, err := store.CreateStaging(fid)
	if err != nil {
		return err
	}
	now := timeutil.Now()
	pending := PendingUpload{Path: path, FID: fid, ParentID: parent.ID, Name: name, LocalPath: localPath, ModTime: now.UnixNano()}
	if err := store.SaveUpload(pending); err != nil {
		_ = store.RemoveStaging(localPath)
		return err
	}
	hashes.Start(pending)
	if hadOld && !old.Frozen {
		store.RemoveStagingIfUnreferenced(old.LocalPath)
		hashes.RemoveFID(old.FID)
	}
	v.setLocalModTime(path, now)
	logging.L.InfofEvery("vfs.pending_created", time.Second, "[VFS] pending created op_id=%q path=%q parent=%q name=%q local=%q", pending.FID, path, parent.ID, name, localPath)
	return nil
}

// rotateFrozenGeneration replaces a frozen pending with a new mutable
// generation seeded with the old staging content, preserving write-at-offset
// semantics. The old pending stays current until the new staging is fully
// prepared; on any failure the new staging is removed and the old pending is
// left untouched. The old frozen staging survives for any in-flight upload.
// Parent/Name are reused from the old pending because the path already went
// through createLocked once.
func (v *VFS) rotateFrozenGeneration(path string, old PendingUpload) (PendingUpload, error) {
	return v.rotateFrozenGenerationWithStore(path, old, newVFSUploadWriteRuntime(v).Store())
}

func (v *VFS) rotateFrozenGenerationWithStore(path string, old PendingUpload, store uploadStoreWriteAdapter) (PendingUpload, error) {
	fid := newStagingFID(path)
	localPath, err := store.CreateStaging(fid)
	if err != nil {
		return PendingUpload{}, err
	}
	size, err := copyStagingContent(old.LocalPath, localPath)
	if err != nil {
		_ = store.RemoveStaging(localPath)
		return PendingUpload{}, err
	}
	now := timeutil.Now()
	pending := PendingUpload{
		Path:      path,
		FID:       fid,
		ParentID:  old.ParentID,
		Name:      old.Name,
		LocalPath: localPath,
		Size:      size,
		ModTime:   now.UnixNano(),
	}
	if err := store.SaveUpload(pending); err != nil {
		_ = store.RemoveStaging(localPath)
		return PendingUpload{}, err
	}
	v.setLocalModTime(path, now)
	return pending, nil
}

func copyStagingContent(srcPath, dstPath string) (int64, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer src.Close()
	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		return 0, copyErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	return written, nil
}

func (v *VFS) WriteAt(ctx context.Context, path string, data []byte, off int64) (n int, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpWrite, err) }()
	path = cleanVirtual(path)
	unlock := v.lockPath(path)
	defer unlock()
	runtime := newVFSUploadWriteRuntime(v)
	store := runtime.Store()
	hashes := runtime.HashTracker()
	pending, err := pendingUploadFromWriteStore(store, path)
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
		pending, err = pendingUploadFromWriteStore(store, path)
		if err != nil {
			return 0, err
		}
	}
	if pending.Frozen {
		pending, err = v.rotateFrozenGeneration(path, pending)
		if err != nil {
			return 0, err
		}
	}
	n, err = store.WriteStagingAt(pending.LocalPath, data, off)
	if err != nil {
		return n, err
	}
	hashes.Write(pending, data[:n], off)
	if writtenEnd := off + int64(n); writtenEnd > pending.Size {
		pending.Size = writtenEnd
	}
	now := timeutil.Now()
	pending.ModTime = now.UnixNano()
	store.UpdateUploadTransient(pending)
	v.setLocalModTime(path, now)
	logging.L.DebugfEvery("vfs.write_staged", time.Second, "[VFS] write staged op_id=%q path=%q off=%d len=%d written=%d size=%d local=%q", pending.FID, path, off, len(data), n, pending.Size, pending.LocalPath)
	return n, nil
}

func (v *VFS) Flush(ctx context.Context, path string) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpWrite, err) }()
	path = cleanVirtual(path)
	unlock := v.lockPath(path)
	defer unlock()
	store := newVFSUploadWriteRuntime(v).Store()
	pending, err := pendingUploadFromWriteStore(store, path)
	if err != nil {
		logging.L.DebugfEvery("vfs.flush_ignored", time.Second, "[VFS] flush ignored without pending path=%q", path)
		return nil
	}
	if err := store.FlushStaging(pending.LocalPath); err != nil {
		return err
	}
	if err := store.SyncStaging(pending.LocalPath); err != nil {
		return err
	}
	size, err := store.StagingSize(pending.LocalPath)
	if err != nil {
		return err
	}
	pending.Size = size
	pending.Frozen = true
	if pending.ModTime == 0 {
		now := timeutil.Now()
		pending.ModTime = now.UnixNano()
		v.setLocalModTime(path, now)
	}
	if err := store.SaveUpload(pending); err != nil {
		return err
	}
	if latest, ok := store.UploadByPath(path); ok {
		pending = latest
	}
	delay := v.uploads.delay
	if pending.Size == 0 && delay < zeroByteUploadDebounceDelay {
		delay = zeroByteUploadDebounceDelay
	}
	logging.L.InfofEvery("vfs.flush_queued", time.Second, "[VFS] flush queued upload op_id=%q path=%q name=%q size=%d local=%q delay=%s", pending.FID, pending.Path, pending.Name, pending.Size, pending.LocalPath, delay)
	v.enqueueAfter(pending, delay)
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
	runtime := newVFSUploadWriteRuntime(v)
	store := runtime.Store()
	hashes := runtime.HashTracker()
	pending, err := pendingUploadFromWriteStore(store, path)
	if err != nil {
		if err := v.stageExisting(ctx, path); err != nil {
			return err
		}
		pending, err = pendingUploadFromWriteStore(store, path)
		if err != nil {
			return err
		}
	}
	if pending.Frozen {
		pending, err = v.rotateFrozenGeneration(path, pending)
		if err != nil {
			return err
		}
	}
	if err := store.TruncateStaging(pending.LocalPath, size); err != nil {
		return err
	}
	hashes.Dirty(pending)
	pending.Size = size
	now := timeutil.Now()
	pending.ModTime = now.UnixNano()
	v.setLocalModTime(path, now)
	if entry, err := v.resolve(ctx, path); err == nil && !entry.IsDir {
		v.invalidateReadCache(entry)
	}
	return store.SaveUpload(pending)
}

func (v *VFS) stageExisting(ctx context.Context, path string) error {
	return v.stageExistingWithStore(ctx, path, newVFSUploadWriteRuntime(v).Store())
}

func (v *VFS) stageExistingWithStore(ctx context.Context, path string, store uploadStoreWriteAdapter) error {
	return v.stageExistingWithDeps(ctx, path, store, newVFSUploadWriteRuntime(v).Remote())
}

func (v *VFS) stageExistingWithDeps(ctx context.Context, path string, store uploadStoreWriteAdapter, remote uploadWriteRemote) error {
	parent, name, err := remote.Parent(ctx, path)
	if err != nil {
		return err
	}
	fid := newStagingFID(path)
	localPath, err := store.CreateStaging(fid)
	if err != nil {
		return err
	}
	if entry, err := remote.Resolve(ctx, path); err == nil && !entry.IsDir {
		rc, err := remote.Read(ctx, entry)
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
	size, _ := store.StagingSize(localPath)
	modTime := timeutil.Now()
	if entry, err := remote.Resolve(ctx, path); err == nil && !entry.ModTime.IsZero() {
		modTime = entry.ModTime
	}
	pending := PendingUpload{
		Path:      path,
		FID:       fid,
		ParentID:  parent.ID,
		Name:      name,
		LocalPath: localPath,
		Size:      size,
		ModTime:   modTime.UnixNano(),
	}
	if err := store.SaveUpload(pending); err != nil {
		return err
	}
	logging.L.InfofEvery("vfs.existing_file_staged", time.Second, "[VFS] existing file staged op_id=%q path=%q parent=%q name=%q size=%d local=%q", pending.FID, path, parent.ID, name, size, localPath)
	return nil
}

func (v *VFS) SetModTime(ctx context.Context, path string, modTime time.Time) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpWrite, err) }()
	path = cleanVirtual(path)
	if modTime.IsZero() {
		return nil
	}
	unlock := v.lockPath(path)
	defer unlock()
	store := newVFSUploadWriteRuntime(v).Store()
	if _, err := pendingUploadFromWriteStore(store, path); err == nil {
		v.setLocalModTime(path, modTime)
		return nil
	}
	if entry, err := v.resolve(ctx, path); err != nil {
		return err
	} else {
		newVFSViewRuntime(v).CommitEntryLocalModTime(path, entry, modTime)
	}
	return nil
}
