package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"path/filepath"
	"strings"
	"time"
)

func (v *VFS) PrepareDirectoryCopy(ctx context.Context, path string) error {
	return v.prepareDirectoryCopyWithRuntime(ctx, path, newVFSDirectoryCopyRuntime(v))
}

func (v *VFS) prepareDirectoryCopyWithRuntime(ctx context.Context, path string, runtime vfsDirectoryCopyRuntime) error {
	path = cleanVirtual(path)
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	if !entry.IsDir {
		return fmt.Errorf("vfs: %s is not a directory", path)
	}
	hideNames := map[string]time.Time{}
	if entries, err := runtime.ListChildren(ctx, entry.ID); err == nil {
		expires := time.Now().Add(directoryCopyHideTTL)
		for _, child := range entries {
			if !isAppleMetadataName(child.Name) {
				hideNames[child.Name] = expires
			}
		}
	}
	if err := runtime.CleanupPendingChildren(path); err != nil {
		return err
	}
	runtime.PrepareLocalDirectoryCopy(path, hideNames)
	return nil
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

func (v *VFS) Mkdir(ctx context.Context, path string) (entry drive.Entry, err error) {
	return v.mkdirWithDeps(ctx, path, newVFSDriverRuntime(v).MutationBackend(), newVFSViewCommitter(v))
}

func (v *VFS) mkdirWithDeps(ctx context.Context, path string, backend mutationBackend, committer ViewCommitter) (entry drive.Entry, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpMkdir, err) }()
	if err := newVFSDriverRuntime(v).RequireCapability(drive.CapabilityWriter, "mkdir"); err != nil {
		return drive.Entry{}, err
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
	entry, err = backend.Mkdir(ctx, parent.ID, name)
	if err != nil {
		if !isAlreadyExistsError(err) {
			logging.L.Warnf("[VFS] mkdir failed path=%q parent=%q name=%q err=%v", path, parent.ID, name, err)
			return drive.Entry{}, err
		}
		logging.L.InfofEvery("vfs.mkdir_already_exists", time.Second, "[VFS] mkdir already exists; resolving existing directory path=%q parent=%q name=%q", path, parent.ID, name)
		entry, err = findExistingChildDir(ctx, backend, committer, filepath.Dir(path), parent.ID, name)
		if err != nil {
			logging.L.Warnf("[VFS] mkdir existing directory resolve failed path=%q parent=%q name=%q err=%v", path, parent.ID, name, err)
			return drive.Entry{}, err
		}
	}
	committer.CommitMkdir(path, entry)
	logging.L.InfofEvery("vfs.mkdir_complete", time.Second, "[VFS] mkdir complete path=%q id=%q parent=%q", path, entry.ID, entry.ParentID)
	return entry, nil
}

func findExistingChildDir(ctx context.Context, backend mutationBackend, committer ViewCommitter, parentPath, parentID, name string) (drive.Entry, error) {
	entries, err := backend.List(ctx, parentID)
	if err != nil {
		return drive.Entry{}, err
	}
	committer.CacheListedChildren(parentPath, entries)
	for _, child := range entries {
		if child.Name == name && child.IsDir {
			return child, nil
		}
	}
	return drive.Entry{}, fmt.Errorf("vfs: existing directory not found: %s", joinVirtual(parentPath, name))
}
func (v *VFS) Remove(ctx context.Context, path string) (err error) {
	return v.removeWithRuntime(ctx, path, newVFSRemoveRuntime(v))
}

func (v *VFS) removeWithRuntime(ctx context.Context, path string, runtime vfsRemoveRuntime) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpDelete, err) }()
	if err := newVFSDriverRuntime(v).RequireCapability(drive.CapabilityWriter, "remove"); err != nil {
		return err
	}
	path = cleanVirtual(path)
	if _, err := v.pendingUpload(path); err == nil {
		unlock := v.lockPath(path)
		defer unlock()
		return runtime.RemovePendingUpload(path)
	}
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	runtime.InvalidateReadCache(entry)
	runtime.MarkDeleted(path, entry)
	runtime.ClearLocalModTime(path)
	logging.L.Infof("[VFS] remove queued path=%q id=%q dir=%t delay=%s", path, entry.ID, entry.IsDir, v.deletes.delay)
	v.scheduleDelete(path, entry)
	return nil
}
func (v *VFS) RemoveDir(ctx context.Context, path string) (err error) {
	return v.removeDirWithRuntime(ctx, path, newVFSRemoveRuntime(v))
}

func (v *VFS) removeDirWithRuntime(ctx context.Context, path string, runtime vfsRemoveRuntime) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpDelete, err) }()
	if err := newVFSDriverRuntime(v).RequireCapability(drive.CapabilityWriter, "remove"); err != nil {
		return err
	}
	path = cleanVirtual(path)
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	if !entry.IsDir {
		return fmt.Errorf("vfs: %s is not a directory", path)
	}
	runtime.InvalidateReadCache(entry)
	runtime.CancelChildUploads(path)
	if err := runtime.RemovePendingUploadsUnder(path); err != nil {
		return err
	}
	runtime.CancelChildDeletes(path)
	runtime.MarkDeleted(path, entry)
	runtime.ClearLocalModTime(path)
	logging.L.Infof("[VFS] remove dir queued path=%q id=%q delay=%s", path, entry.ID, v.deletes.delay)
	v.scheduleDelete(path, entry)
	return nil
}
func (v *VFS) Rename(ctx context.Context, oldPath, newPath string) (err error) {
	return v.renameWithDeps(ctx, oldPath, newPath, newVFSDriverRuntime(v).MutationBackend(), newVFSMutationRuntime(v))
}

func (v *VFS) renameWithDeps(ctx context.Context, oldPath, newPath string, backend mutationBackend, runtime mutationRuntime) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpRename, err) }()
	if err := newVFSDriverRuntime(v).RequireCapability(drive.CapabilityWriter, "rename"); err != nil {
		return err
	}
	oldPath = cleanVirtual(oldPath)
	newPath = cleanVirtual(newPath)
	if oldPath == "/" || newPath == "/" {
		return fmt.Errorf("vfs: cannot rename root")
	}

	if pending, err := v.pendingUpload(oldPath); err == nil {
		unlockOld := v.lockPath(oldPath)
		defer unlockOld()
		parent, name, err := v.parent(ctx, newPath)
		if err != nil {
			return err
		}
		pending.Path = newPath
		pending.ParentID = parent.ID
		pending.Name = name
		if err := runtime.RenamePendingUpload(oldPath, newPath, pending); err != nil {
			return err
		}
		return nil
	}

	entry, err := v.resolve(ctx, oldPath)
	if err != nil {
		return err
	}
	runtime.InvalidateReadCache(entry)
	dstParent, newName, err := v.parent(ctx, newPath)
	if err != nil {
		return err
	}
	oldParent := filepath.Dir(oldPath)
	newParent := filepath.Dir(newPath)
	if filepath.Base(oldPath) != newName {
		if err := backend.Rename(ctx, entry, newName); err != nil {
			return err
		}
		entry.Name = newName
	}
	if oldParent != newParent {
		if err := backend.Move(ctx, entry, dstParent.ID); err != nil {
			return err
		}
		entry.ParentID = dstParent.ID
	}
	entry.Name = newName
	entry.ParentID = dstParent.ID
	runtime.CommitRemoteRename(oldPath, newPath, entry)
	return nil
}

type mutationBackend interface {
	List(ctx context.Context, parentID string) ([]drive.Entry, error)
	Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error)
	Rename(ctx context.Context, entry drive.Entry, newName string) error
	Move(ctx context.Context, entry drive.Entry, dstParentID string) error
}

type driverMutationBackend struct {
	driver drive.Driver
}

func newDriverMutationBackend(driver drive.Driver) driverMutationBackend {
	return driverMutationBackend{driver: driver}
}

func (b driverMutationBackend) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return b.driver.List(ctx, parentID)
}

func (b driverMutationBackend) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	return b.driver.Mkdir(ctx, parentID, name)
}

func (b driverMutationBackend) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	return b.driver.Rename(ctx, entry, newName)
}

func (b driverMutationBackend) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	return b.driver.Move(ctx, entry, dstParentID)
}

// ViewCommitter is the narrow mutation-commit boundary: it writes the
// effective view after a local mutation. The mutation coordinator depends
// on this interface (never on view.mu/entries/localDirs/lists), so commit
// semantics are testable against a stub and the view structure can evolve
// underneath.
type ViewCommitter interface {
	CommitMkdir(path string, entry drive.Entry)
	CacheListedChildren(parentPath string, entries []drive.Entry)
}

// vfsViewCommitter implements ViewCommitter over the VFS view.
type vfsViewCommitter struct {
	v *VFS
}

func newVFSViewCommitter(v *VFS) vfsViewCommitter {
	return vfsViewCommitter{v: v}
}

// CommitMkdir is the mutation-commit entry point for a created directory.
// It is the canonical shape the mutation coordinator will use for every
// local mutation (Rename/Remove/UploadCommitted follow the same pattern):
// write the entry cache, mark derived local state, and invalidate the
// affected list cache - all under the view lock, so a concurrent reader
// never observes a half-committed mutation.
func (r vfsViewCommitter) CommitMkdir(path string, entry drive.Entry) {
	r.v.view.mu.Lock()
	r.v.view.entries.Set(path, entry)
	r.v.markLocalDirLocked(path)
	r.v.invalidateListLocked(filepath.Dir(path))
	r.v.view.mu.Unlock()
}

func (r vfsViewCommitter) CacheListedChildren(parentPath string, entries []drive.Entry) {
	r.v.view.mu.Lock()
	defer r.v.view.mu.Unlock()
	for _, child := range entries {
		r.v.view.entries.Set(joinVirtual(parentPath, child.Name), child)
	}
}

type mutationRuntime interface {
	CommitRemoteRename(oldPath, newPath string, entry drive.Entry) drive.Entry
	InvalidateReadCache(entry drive.Entry)
	RenamePendingUpload(oldPath, newPath string, pending PendingUpload) error
}

type vfsMutationRuntime struct {
	v *VFS
}

func newVFSMutationRuntime(v *VFS) vfsMutationRuntime {
	return vfsMutationRuntime{v: v}
}

func (r vfsMutationRuntime) CacheListedChildren(parentPath string, entries []drive.Entry) {
	r.v.view.mu.Lock()
	defer r.v.view.mu.Unlock()
	for _, child := range entries {
		r.v.view.entries.Set(joinVirtual(parentPath, child.Name), child)
	}
}

// CommitMkdir is the mutation-commit entry point for a created directory.
// It is the canonical shape the mutation coordinator will use for every
// local mutation (Rename/Remove/UploadCommitted follow the same pattern):
// write the entry cache, mark derived local state, and invalidate the
// affected list cache - all under the view lock, so a concurrent reader
// never observes a half-committed mutation.
func (r vfsMutationRuntime) CommitMkdir(path string, entry drive.Entry) {
	r.v.view.mu.Lock()
	r.v.view.entries.Set(path, entry)
	r.v.markLocalDirLocked(path)
	r.v.invalidateListLocked(filepath.Dir(path))
	r.v.view.mu.Unlock()
}

func (r vfsMutationRuntime) CommitRemoteRename(oldPath, newPath string, entry drive.Entry) drive.Entry {
	oldParent := filepath.Dir(oldPath)
	newParent := filepath.Dir(newPath)
	r.v.view.mu.Lock()
	r.v.view.entries.Delete(oldPath)
	r.v.view.entries.Delete(newPath)
	r.v.rebaseCachedPathsLocked(oldPath, newPath)
	r.v.moveLocalModTimeLocked(oldPath, newPath)
	r.v.invalidateListLocked(oldParent)
	r.v.invalidateListLocked(newParent)
	entry = r.v.applyLocalModTimeLocked(newPath, entry)
	r.v.view.entries.Set(newPath, entry)
	r.v.view.mu.Unlock()
	r.v.addOverlay(oldPath, newPath, entry.ID, entry.IsDir)
	return entry
}

func (r vfsMutationRuntime) InvalidateReadCache(entry drive.Entry) {
	r.v.invalidateReadCache(entry)
}

func (r vfsMutationRuntime) RenamePendingUpload(oldPath, newPath string, pending PendingUpload) error {
	r.v.moveLocalModTime(oldPath, newPath)
	if err := r.v.uploads.Store().RenameUpload(oldPath, pending); err != nil {
		return err
	}
	r.v.hashes.renamePath(oldPath, newPath, pending)
	return nil
}

type vfsRemoveRuntime struct {
	v *VFS
}

func newVFSRemoveRuntime(v *VFS) vfsRemoveRuntime {
	return vfsRemoveRuntime{v: v}
}

func (r vfsRemoveRuntime) InvalidateReadCache(entry drive.Entry) {
	r.v.invalidateReadCache(entry)
}

func (r vfsRemoveRuntime) RemovePendingUpload(path string) error {
	r.v.cancelUpload(path)
	r.v.clearLocalModTime(path)
	if err := r.v.uploads.Store().RemoveUpload(path); err != nil {
		return err
	}
	r.v.hashes.removePath(path)
	return nil
}

func (r vfsRemoveRuntime) RemovePendingUploadsUnder(path string) error {
	if err := r.v.uploads.Store().RemoveUploadsUnder(path); err != nil {
		return err
	}
	r.v.hashes.removeUnder(path)
	return nil
}

func (r vfsRemoveRuntime) MarkDeleted(path string, entry drive.Entry) {
	r.v.markDeleted(path, entry)
}

func (r vfsRemoveRuntime) ClearLocalModTime(path string) {
	r.v.clearLocalModTime(path)
}

func (r vfsRemoveRuntime) CancelChildUploads(path string) {
	r.v.uploads.CancelChildUploads(path)
}

func (r vfsRemoveRuntime) CancelChildDeletes(path string) {
	r.v.cancelChildDeletes(path)
}

type vfsDirectoryCopyRuntime struct {
	v *VFS
}

func newVFSDirectoryCopyRuntime(v *VFS) vfsDirectoryCopyRuntime {
	return vfsDirectoryCopyRuntime{v: v}
}

func (r vfsDirectoryCopyRuntime) ListChildren(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return newVFSDriverRuntime(r.v).List(ctx, parentID)
}

func (r vfsDirectoryCopyRuntime) CleanupPendingChildren(path string) error {
	r.v.uploads.CancelChildUploads(path)
	if err := r.v.uploads.Store().RemoveUploadsUnder(path); err != nil {
		return err
	}
	r.v.hashes.removeUnder(path)
	return nil
}

func (r vfsDirectoryCopyRuntime) PrepareLocalDirectoryCopy(path string, hideNames map[string]time.Time) {
	r.v.view.entries.Range(func(cachedPath string, cachedEntry drive.Entry) bool {
		if filepath.Dir(cachedPath) == path {
			if _, ok := hideNames[cachedEntry.Name]; !ok && !isAppleMetadataName(cachedEntry.Name) {
				hideNames[cachedEntry.Name] = time.Now().Add(directoryCopyHideTTL)
			}
			r.v.view.entries.Delete(cachedPath)
		}
		return true
	})
	r.v.view.mu.Lock()
	r.v.markLocalDirLocked(path)
	r.v.invalidateListLocked(path)
	r.v.view.mu.Unlock()
	r.v.setCopyHidden(path, hideNames)
}
