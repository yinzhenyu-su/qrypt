package vfs

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
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

// newVFSViewCommitter wires the view-domain committer (pkg/vfs/view) to the
// VFS read-cache side effects: entry invalidation and staging-file seeding.
// The commit semantics live in the view package; this adapter only supplies
// the two cross-domain functions.
func newVFSViewCommitter(v *VFS) view.Committer {
	return view.NewCommitter(v.view, newVFSVisibilityRuntime(v), v.invalidateReadCache, v.seedReadCacheFromStaging)
}

// Compile-time assertions: the view committer serves both the commit
// boundary and the cache-warming boundary.
var _ viewCommitter = view.Committer{}
var _ listedChildrenCache = view.Committer{}

// mutationRuntime is the rename-time local-state surface: pending-store
// rename and read-cache invalidation. The remote-view commit lives on
// viewCommitter.
type mutationRuntime interface {
	InvalidateReadCache(entry drive.Entry)
	RenamePendingUpload(oldPath, newPath string, pending PendingUpload) error
}

// readCacheInvalidator drops a committed entry's read-cache state. *VFS
// satisfies it via its package-private invalidateReadCache method; mutation
// and upload-write adapters depend on this narrow boundary instead of the
// whole VFS.
type readCacheInvalidator interface {
	invalidateReadCache(entry drive.Entry)
}

type vfsMutationRuntime struct {
	invalidator readCacheInvalidator
	viewRT      view.Runtime
	store       *uploadStore
	hashes      *upload.HashTracker
}

func newVFSMutationRuntime(v *VFS) vfsMutationRuntime {
	return vfsMutationRuntime{
		invalidator: v,
		viewRT:      view.NewRuntime(v.view),
		store:       v.uploads.Store(),
		hashes:      v.hashes,
	}
}

func (r vfsMutationRuntime) InvalidateReadCache(entry drive.Entry) {
	r.invalidator.invalidateReadCache(entry)
}

func (r vfsMutationRuntime) RenamePendingUpload(oldPath, newPath string, pending PendingUpload) error {
	r.viewRT.MoveLocalModTime(oldPath, newPath)
	if err := r.store.RenameUpload(oldPath, pending); err != nil {
		return err
	}
	r.hashes.RenamePath(oldPath, newPath, pending)
	return nil
}
