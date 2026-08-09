package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/internal/vfs/mutation"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"path/filepath"
)

func (v *VFS) PrepareDirectoryCopy(ctx context.Context, path string) error {
	return v.prepareDirectoryCopyWithRuntime(ctx, path, newVFSDirectoryCopyRuntime(v))
}

func (v *VFS) Mkdir(ctx context.Context, path string) (entry drive.Entry, err error) {
	return v.mkdirWithDeps(ctx, path, newVFSDriverRuntime(v).MutationBackend(), newVFSViewCommitter(v), newVFSViewCommitter(v))
}

// mkdirWithDeps drives directory creation through the mutation mkdir
// coordinator; the VFS shell keeps the capability gate, health recording,
// and adapter construction.

func (v *VFS) Remove(ctx context.Context, path string) (err error) {
	return v.removeWithRuntime(ctx, path, newVFSViewCommitter(v))
}

func (v *VFS) RemoveDir(ctx context.Context, path string) (err error) {
	return v.removeDirWithRuntime(ctx, path, newVFSViewCommitter(v))
}

func (v *VFS) Rename(ctx context.Context, oldPath, newPath string) (err error) {
	return v.renameWithDeps(ctx, oldPath, newPath, newVFSDriverRuntime(v).MutationBackend(), newVFSMutationRuntime(v), newVFSViewCommitter(v))
}

// renameWithDeps drives the rename through the mutation coordinator; the
// VFS shell keeps the capability gate and health recording, and builds the
// adapters.

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
