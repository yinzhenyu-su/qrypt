package view

import (
	"path/filepath"

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
	rt.InvalidateListLocked(filepath.Dir(path))
	r.view.mu.Unlock()
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
	r.vis.UnhideCopyChild(filepath.Dir(path), entry.Name)
	rt.InvalidateListLocked(filepath.Dir(path))
	r.view.mu.Unlock()
}

// CommitRemoteRename folds a completed remote rename/move into the view: it
// removes the old path (rebasing cached descendants), moves local modtime,
// invalidates the affected parent list caches, writes the new entry, and
// records the rename overlay so stale backend listings hide the old name.
func (r Committer) CommitRemoteRename(oldPath, newPath string, entry drive.Entry) {
	oldParent := filepath.Dir(oldPath)
	newParent := filepath.Dir(newPath)
	rt := NewRuntime(r.view)
	r.view.mu.Lock()
	r.view.entries.Delete(oldPath)
	r.view.entries.Delete(newPath)
	rt.RebaseCachedPathsLocked(oldPath, newPath)
	rt.MoveLocalModTimeLocked(oldPath, newPath)
	rt.InvalidateListLocked(oldParent)
	rt.InvalidateListLocked(newParent)
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
