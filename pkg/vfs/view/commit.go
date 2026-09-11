package view

import (
	pathpkg "path"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Committer is the mutation-commit boundary: it writes the effective view
// state after a local mutation (mkdir / remove / upload / rename), farming
// out the two cross-domain side effects - read-cache invalidation and
// read-cache seeding - to injected functions. pkg/vfs wires those to the
// read-cache domain; the coordinator packages (mutation, upload) depend on
// the exported method surface, never on the View internals.
type Committer struct {
	view       *View
	vis        Visibility
	invalidate func(entry drive.Entry)
	seed       func(entry drive.Entry, stagingPath string)
}

// NewCommitter builds the commit boundary. invalidate drops a committed
// entry's read-cache state; seed warms the read cache from a staging file
// (hence CommitUploadedEntry only calls it when stagingPath is non-empty).
func NewCommitter(view *View, vis Visibility, invalidate func(entry drive.Entry), seed func(entry drive.Entry, stagingPath string)) Committer {
	return Committer{view: view, vis: vis, invalidate: invalidate, seed: seed}
}

// CommitMkdir is the mutation-commit entry point for a created directory:
// write the entry cache, mark derived local state, and invalidate the
// affected list cache - all under the view lock, so a concurrent reader
// never observes a half-committed mutation.
func (r Committer) CommitMkdir(path string, entry drive.Entry) {
	rt := NewRuntime(r.view)
	r.view.mu.Lock()
	r.view.entries.Set(path, entry)
	rt.MarkLocalDirLocked(path)
	rt.InvalidateListLocked(pathpkg.Dir(path))
	r.view.mu.Unlock()
	r.vis.RetireRenameShadow(path, entry.ID)
}

// CommitRemove marks a path deleted in the view: the visibility overlay hides
// it (and its subtree for directories), the entry cache drops it, the read
// cache is invalidated, and local modtime is cleared. The delayed remote
// delete runs later through the delete executor.
func (r Committer) CommitRemove(path string, entry drive.Entry) {
	r.vis.MarkDeleted(path, entry)
	r.invalidate(entry)
	NewRuntime(r.view).ClearLocalModTime(path)
}

// CommitUploadedEntry folds a completed upload into the view: it seeds the
// read cache from the staging file (when one exists), writes the uploaded
// entry, unhides the copy child, and invalidates the parent list cache.
func (r Committer) CommitUploadedEntry(path string, entry drive.Entry, stagingPath string) {
	if stagingPath != "" {
		r.seed(entry, stagingPath)
	}
	rt := NewRuntime(r.view)
	r.view.mu.Lock()
	r.view.entries.Set(path, entry)
	r.vis.UnhideCopyChild(pathpkg.Dir(path), entry.Name)
	rt.InvalidateListLocked(pathpkg.Dir(path))
	r.view.mu.Unlock()
	r.vis.RetireRenameShadow(path, entry.ID)
}

// DropUploadedEntry takes a just-committed upload entry back out of the view:
// the entry cache drops the path, the read cache state for the entry is
// invalidated, and the parent list cache is invalidated so the next listing
// reflects whatever now owns the path. It is the rollback half of
// CommitUploadedEntry, used when the pending record moved on before the commit
// could be claimed.
func (r Committer) DropUploadedEntry(path string, entry drive.Entry) {
	r.invalidate(entry)
	rt := NewRuntime(r.view)
	r.view.mu.Lock()
	r.view.entries.Delete(path)
	rt.InvalidateListLocked(pathpkg.Dir(path))
	r.view.mu.Unlock()
}

// CommitRemoteRename folds a completed remote rename/move into the view: it
// removes the old path, re-keys or drops cached descendants depending on
// whether the backend reported a new identity, moves local modtime and
// local-directory markers, invalidates the affected list caches, writes the
// new entry, and records the rename overlay so stale backend listings hide
// the old name.
func (r Committer) CommitRemoteRename(oldPath, newPath string, entry drive.Entry) {
	oldParent := pathpkg.Dir(oldPath)
	newParent := pathpkg.Dir(newPath)
	rt := NewRuntime(r.view)
	r.view.mu.Lock()
	previous, hadPrevious := r.view.entries.Get(vfstypes.CleanVirtualPath(oldPath))
	r.view.entries.Delete(oldPath)
	r.view.entries.Delete(newPath)
	if hadPrevious && previous.ID != "" && previous.ID != entry.ID {
		// The backend reported a different identity for the renamed object,
		// which means every cached descendant identity went stale with it:
		// drop the subtree instead of re-keying entries that can no longer
		// address the backend. The next resolve or listing refills them.
		r.view.entries.DeleteUnder(oldPath)
		rt.DropListCachesUnderLocked(oldPath)
	} else {
		rt.RebaseCachedPathsLocked(oldPath, newPath)
	}
	rt.MoveLocalModTimeLocked(oldPath, newPath)
	rt.RebaseLocalDirsLocked(oldPath, newPath)
	rt.InvalidateListLocked(oldParent)
	rt.InvalidateListLocked(newParent)
	// The renamed directory's own listing cache is stale too (a rename over
	// an existing target would otherwise serve the replaced directory's
	// children until the cache expires).
	rt.InvalidateListLocked(newPath)
	entry = rt.ApplyLocalModTimeLocked(newPath, entry)
	r.view.entries.Set(newPath, entry)
	r.view.mu.Unlock()
	r.vis.AddRenameOverlay(oldPath, newPath, entry.ID, entry.IsDir)
}

// CacheListedChildren warms the entry cache with a freshly fetched remote
// listing (query recovery / cache warming, not a mutation commit).
func (r Committer) CacheListedChildren(parentPath string, entries []drive.Entry) {
	r.view.mu.Lock()
	defer r.view.mu.Unlock()
	for _, child := range entries {
		r.view.entries.Set(vfstypes.JoinVirtualPath(parentPath, child.Name), child)
	}
}
