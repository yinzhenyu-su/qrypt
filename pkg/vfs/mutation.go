package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/vfs/mutation"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"path/filepath"
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

func (v *VFS) Mkdir(ctx context.Context, path string) (entry drive.Entry, err error) {
	return v.mkdirWithDeps(ctx, path, newVFSDriverRuntime(v).MutationBackend(), newVFSViewCommitter(v), newVFSViewCommitter(v))
}

// mkdirWithDeps drives directory creation through the mutation mkdir
// coordinator; the VFS shell keeps the capability gate, health recording,
// and adapter construction.
func (v *VFS) mkdirWithDeps(ctx context.Context, path string, backend mutationBackend, committer viewCommitter, cache listedChildrenCache) (entry drive.Entry, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpMkdir, err) }()
	if err := newVFSDriverRuntime(v).RequireCapability(drive.CapabilityWriter, "mkdir"); err != nil {
		return drive.Entry{}, err
	}
	coordinator := mutation.NewMkdirCoordinator(
		newVFSRenameResolver(v),
		backend,
		newVFSMkdirView(v, committer, cache),
	)
	return coordinator.Mkdir(ctx, path)
}

// vfsMkdirView adapts VFS overlay restoration + view commits to
// mutation.MkdirView. driverMutationBackend already satisfies
// mutation.MkdirRemote (List/Mkdir); vfsRenameResolver satisfies
// mutation.MkdirResolver (Resolve/Parent).
type vfsMkdirView struct {
	v         *VFS
	committer viewCommitter
	cache     listedChildrenCache
}

func newVFSMkdirView(v *VFS, committer viewCommitter, cache listedChildrenCache) vfsMkdirView {
	return vfsMkdirView{v: v, committer: committer, cache: cache}
}

func (r vfsMkdirView) RestoreDeleted(path string) (drive.Entry, bool) {
	return r.v.restoreDeletedPath(path)
}

func (r vfsMkdirView) RestoreDeletedAncestor(path string) {
	r.v.restoreDeletedAncestor(path)
}

func (r vfsMkdirView) IsUnderRestoredDir(path string) bool {
	return r.v.isUnderRestoredDir(path)
}

func (r vfsMkdirView) CommitMkdir(path string, entry drive.Entry) {
	r.committer.CommitMkdir(path, entry)
}

func (r vfsMkdirView) CacheListedChildren(parentPath string, entries []drive.Entry) {
	r.cache.CacheListedChildren(parentPath, entries)
}

// Compile-time assertions for the mkdir adapters.
var _ mutation.MkdirRemote = driverMutationBackend{}
var _ mutation.MkdirResolver = vfsRenameResolver{}
var _ mutation.MkdirView = vfsMkdirView{}

func (v *VFS) Remove(ctx context.Context, path string) (err error) {
	return v.removeWithRuntime(ctx, path, newVFSViewCommitter(v))
}

func (v *VFS) removeWithRuntime(ctx context.Context, path string, committer viewCommitter) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpDelete, err) }()
	if err := newVFSDriverRuntime(v).RequireCapability(drive.CapabilityWriter, "remove"); err != nil {
		return err
	}
	coordinator := mutation.NewRemoveCoordinator(
		newVFSRenameResolver(v),
		committer,
		newVFSRemoveScheduler(v),
		newVFSRemoveCleanup(v),
	)
	return coordinator.RemoveFile(ctx, path)
}
func (v *VFS) RemoveDir(ctx context.Context, path string) (err error) {
	return v.removeDirWithRuntime(ctx, path, newVFSViewCommitter(v))
}

func (v *VFS) removeDirWithRuntime(ctx context.Context, path string, committer viewCommitter) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpDelete, err) }()
	if err := newVFSDriverRuntime(v).RequireCapability(drive.CapabilityWriter, "remove"); err != nil {
		return err
	}
	coordinator := mutation.NewRemoveCoordinator(
		newVFSRenameResolver(v),
		committer,
		newVFSRemoveScheduler(v),
		newVFSRemoveCleanup(v),
	)
	return coordinator.RemoveDirectory(ctx, path)
}

// vfsRemoveScheduler adapts the VFS delayed delete to mutation.DeleteScheduler.
type vfsRemoveScheduler struct{ v *VFS }

func newVFSRemoveScheduler(v *VFS) vfsRemoveScheduler { return vfsRemoveScheduler{v: v} }

func (r vfsRemoveScheduler) ScheduleDelete(path string, entry drive.Entry) {
	r.v.scheduleDelete(path, entry)
}

// vfsRemoveCleanup adapts the pending-upload removal surface to
// mutation.RemoveCleanup. It owns the pending-store/hash cleanup directly
// (the former vfsRemoveRuntime is inlined here).
type vfsRemoveCleanup struct {
	v *VFS
}

func newVFSRemoveCleanup(v *VFS) vfsRemoveCleanup {
	return vfsRemoveCleanup{v: v}
}

func (r vfsRemoveCleanup) RemovePendingFile(path string) (bool, error) {
	// Take the path lock FIRST and re-check inside it: a pending upload
	// that completes between a bare probe and the lock would otherwise be
	// reported as a pending-only remove while the uploaded remote entry is
	// never committed or scheduled for deletion.
	unlock := r.v.lockPath(path)
	defer unlock()
	if _, err := r.v.pendingUpload(path); err != nil {
		return false, nil
	}
	// Durable store commit FIRST; timer/hash/modtime cleanup runs only
	// after it succeeds, so a failed commit never leaves the path half
	// cleaned.
	if err := r.v.uploads.Store().RemoveUpload(path); err != nil {
		return true, err
	}
	r.v.cancelUpload(path)
	r.v.clearLocalModTime(path)
	r.v.hashes.removePath(path)
	return true, nil
}

func (r vfsRemoveCleanup) PrepareDirectory(path string) error {
	// Durable cleanup (pending store + hashes) comes first; timers are
	// cancelled only after it succeeds, so a failed cleanup never leaves
	// the directory visible with its upload timers already cancelled.
	if err := r.v.uploads.Store().RemoveUploadsUnder(path); err != nil {
		return err
	}
	r.v.hashes.removeUnder(path)
	r.v.uploads.CancelChildUploads(path)
	r.v.cancelChildDeletes(path)
	return nil
}

// Compile-time assertions for the remove adapters.
var _ mutation.RemoveResolver = vfsRenameResolver{}
var _ mutation.RemoveCleanup = vfsRemoveCleanup{}
var _ mutation.DeleteScheduler = vfsRemoveScheduler{}

func (v *VFS) Rename(ctx context.Context, oldPath, newPath string) (err error) {
	return v.renameWithDeps(ctx, oldPath, newPath, newVFSDriverRuntime(v).MutationBackend(), newVFSMutationRuntime(v), newVFSViewCommitter(v))
}

// renameWithDeps drives the rename through the mutation coordinator; the
// VFS shell keeps the capability gate and health recording, and builds the
// adapters.
func (v *VFS) renameWithDeps(ctx context.Context, oldPath, newPath string, backend mutationBackend, runtime mutationRuntime, committer viewCommitter) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpRename, err) }()
	if err := newVFSDriverRuntime(v).RequireCapability(drive.CapabilityWriter, "rename"); err != nil {
		return err
	}
	coordinator := mutation.NewCoordinator(
		newVFSRenameResolver(v),
		newVFSRenamePending(v),
		newVFSRenameView(committer, runtime),
		mutation.NewRemoteRenamer(backend),
	)
	return coordinator.Rename(ctx, oldPath, newPath)
}

// vfsRenameResolver adapts VFS resolution to mutation.Resolver.
type vfsRenameResolver struct{ v *VFS }

func newVFSRenameResolver(v *VFS) vfsRenameResolver { return vfsRenameResolver{v: v} }

func (r vfsRenameResolver) Resolve(ctx context.Context, path string) (drive.Entry, error) {
	return r.v.resolve(ctx, path)
}

func (r vfsRenameResolver) Parent(ctx context.Context, path string) (drive.Entry, string, error) {
	return r.v.parent(ctx, path)
}

// vfsRenamePending adapts the pending-upload rename to mutation.PendingRenamer.
type vfsRenamePending struct{ v *VFS }

func newVFSRenamePending(v *VFS) vfsRenamePending { return vfsRenamePending{v: v} }

func (r vfsRenamePending) IsPending(path string) bool {
	_, err := r.v.pendingUpload(path)
	return err == nil
}

func (r vfsRenamePending) RenamePending(ctx context.Context, oldPath, newPath string, parent drive.Entry, name string) error {
	pending, err := r.v.pendingUpload(oldPath)
	if err != nil {
		return err
	}
	unlockOld := r.v.lockPath(oldPath)
	defer unlockOld()
	pending.Path = newPath
	pending.ParentID = parent.ID
	pending.Name = name
	return newVFSMutationRuntime(r.v).RenamePendingUpload(oldPath, newPath, pending)
}

// vfsRenameView adapts the rename view commit; read-cache invalidation
// stays on the rename-time runtime, the view commit on the committer.
type vfsRenameView struct {
	committer viewCommitter
	runtime   mutationRuntime
}

func newVFSRenameView(committer viewCommitter, runtime mutationRuntime) vfsRenameView {
	return vfsRenameView{committer: committer, runtime: runtime}
}

func (r vfsRenameView) InvalidateReadCache(entry drive.Entry) {
	r.runtime.InvalidateReadCache(entry)
}

func (r vfsRenameView) CommitRemoteRename(oldPath, newPath string, entry drive.Entry) {
	r.committer.CommitRemoteRename(oldPath, newPath, entry)
}

type mutationBackend interface {
	List(ctx context.Context, parentID string) ([]drive.Entry, error)
	Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error)
	Rename(ctx context.Context, entry drive.Entry, newName string) error
	Move(ctx context.Context, entry drive.Entry, dstParentID string) error
}

// driverMutationBackend satisfies mutation.Remote (Rename/Move) for the
// transactional renamer; List/Mkdir remain part of the vfs-local backend
// surface.
var _ mutation.Remote = driverMutationBackend{}

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

// viewCommitter is the narrow mutation-commit boundary: it writes the
// effective view after a local mutation. The mutation coordinator depends
// on this interface (never on view.mu/entries/localDirs/lists), so commit
// semantics are testable against a stub and the view structure can evolve
// underneath. Package-internal: external callers go through the VFS API.
type viewCommitter interface {
	CommitMkdir(path string, entry drive.Entry)
	// CommitRemove marks a path deleted in the view: the visibility overlay
	// hides it (and its subtree for directories), the entry cache drops it,
	// the read cache is invalidated, and local modtime is cleared. The
	// delayed remote delete runs later through the delete executor.
	CommitRemove(path string, entry drive.Entry)
	// CommitUploadedEntry folds a completed upload into the view: it seeds
	// the read cache from the staging file (when one exists), writes the
	// uploaded entry, unhides the copy child, and invalidates the parent
	// list cache.
	CommitUploadedEntry(path string, entry drive.Entry, stagingPath string)
	// CommitRemoteRename folds a completed remote rename/move into the
	// view: it removes the old path (rebasing cached descendants), moves
	// local modtime, invalidates the affected parent list caches, writes
	// the new entry, and records the rename overlay so stale backend
	// listings hide the old name.
	CommitRemoteRename(oldPath, newPath string, entry drive.Entry)
}

// listedChildrenCache warms the entry cache with a freshly fetched remote
// listing. This is query recovery / cache warming, not a mutation commit,
// so it lives on its own narrow interface rather than widening
// viewCommitter.
type listedChildrenCache interface {
	CacheListedChildren(parentPath string, entries []drive.Entry)
}

// vfsViewCommitter implements viewCommitter and listedChildrenCache over
// the VFS view.
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

// Compile-time assertions: the VFS adapter serves both the commit boundary
// and the cache-warming boundary.
var _ viewCommitter = vfsViewCommitter{}
var _ listedChildrenCache = vfsViewCommitter{}

func (r vfsViewCommitter) CommitRemove(path string, entry drive.Entry) {
	r.v.markDeleted(path, entry)
	r.v.invalidateReadCache(entry)
	r.v.clearLocalModTime(path)
}

func (r vfsViewCommitter) CommitUploadedEntry(path string, entry drive.Entry, stagingPath string) {
	if stagingPath != "" {
		r.v.seedReadCacheFromStaging(entry, stagingPath)
	}
	r.v.view.mu.Lock()
	r.v.view.entries.Set(path, entry)
	r.v.unhideCopyChild(filepath.Dir(path), entry.Name)
	r.v.invalidateListLocked(filepath.Dir(path))
	r.v.view.mu.Unlock()
}

func (r vfsViewCommitter) CommitRemoteRename(oldPath, newPath string, entry drive.Entry) {
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
}

// mutationRuntime is the rename-time local-state surface: pending-store
// rename and read-cache invalidation. The remote-view commit lives on
// viewCommitter.
type mutationRuntime interface {
	InvalidateReadCache(entry drive.Entry)
	RenamePendingUpload(oldPath, newPath string, pending PendingUpload) error
}

type vfsMutationRuntime struct {
	v *VFS
}

func newVFSMutationRuntime(v *VFS) vfsMutationRuntime {
	return vfsMutationRuntime{v: v}
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
	// Durable store commit first; timer/hash cleanup runs only after it
	// succeeds, so a failed commit never leaves the children's timers
	// cancelled while their pendings are still visible.
	if err := r.v.uploads.Store().RemoveUploadsUnder(path); err != nil {
		return err
	}
	r.v.uploads.CancelChildUploads(path)
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
