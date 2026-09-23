package view

import (
	pathpkg "path"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs/listing"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Visibility is the sync operation surface over the composite domain: every
// method is one compound critical section over the shared Overlay+DelayedDeleteState lock
// and the View lock (in that order), so a delete commit both marks the
// overlay and unschedules the timer atomically.
type Visibility struct {
	overlay     *Overlay
	deleteState *DelayedDeleteState
	view        *View
	lister      *listing.Lister
}

// NewVisibility builds the sync surface. lister may be nil: it is only used
// by AddRenameOverlay (rename shadow + prefetch suppression).
func NewVisibility(overlay *Overlay, deleteState *DelayedDeleteState, view *View, lister *listing.Lister) Visibility {
	return Visibility{overlay: overlay, deleteState: deleteState, view: view, lister: lister}
}

// MarkDeleted commits a delete into the view: the overlay hides the path
// (and its subtree for directories), the entry cache drops it, the pending
// delete failure/timer/takeover state is cleared, and pending-delete
// restoration markers are dropped.
func (r Visibility) MarkDeleted(path string, entry drive.Entry) {
	r.overlay.mu.Lock()
	r.overlay.setDeleted(path, entry)
	r.deleteState.clearFailureLocked(path)
	r.overlay.removeRenameOverlay(path)
	delete(r.overlay.restoredDirs, path)
	r.overlay.mu.Unlock()

	r.view.mu.Lock()
	r.view.entries.Delete(path)
	if entry.IsDir {
		r.view.entries.DeleteUnder(path)
		for cachedPath := range r.view.lists {
			if cachedPath == path || vfstypes.IsPathUnder(cachedPath, path) {
				delete(r.view.lists, cachedPath)
			}
		}
	}
	r.view.mu.Unlock()
}

// RestoreDeletedPath restores a previously deleted path: the overlay entry
// comes back (with a restored-dir marker for directories), the pending
// delete is unscheduled, and the parent list cache is invalidated.
func (r Visibility) RestoreDeletedPath(path string) (drive.Entry, bool) {
	path = vfstypes.CleanVirtualPath(path)
	r.overlay.mu.Lock()
	entry, ok := r.overlay.deleted[path]
	if !ok {
		r.overlay.mu.Unlock()
		return drive.Entry{}, false
	}
	r.deleteState.clearFailureLocked(path)
	r.deleteState.clearTakeoverLocked(path)
	r.overlay.removeDeleted(path)
	if r.deleteState.unscheduleLocked(path) {
		logging.L.Infof("[VFS] canceled pending delete for restored path=%q id=%q", path, entry.ID)
	}
	if entry.IsDir {
		r.overlay.restoredDirs[path] = time.Now().Add(RestoredDirTTL)
	}
	r.overlay.mu.Unlock()

	r.view.mu.Lock()
	r.view.entries.Set(path, entry)
	delete(r.view.lists, vfstypes.CleanVirtualPath(pathpkg.Dir(path)))
	r.view.mu.Unlock()
	return entry, true
}

// RestoreDeletedAncestor restores the deepest deleted directory ancestor of
// path (children under an outer deleted dir stay hidden).
func (r Visibility) RestoreDeletedAncestor(path string) {
	path = vfstypes.CleanVirtualPath(path)
	r.overlay.mu.Lock()
	restorePath, entry, ok := r.overlay.deepestDeletedAncestor(path)
	if !ok {
		r.overlay.mu.Unlock()
		return
	}
	r.overlay.removeDeleted(restorePath)
	r.deleteState.clearFailureLocked(restorePath)
	r.deleteState.clearTakeoverLocked(restorePath)
	if r.deleteState.unscheduleLocked(restorePath) {
		logging.L.Infof("[VFS] canceled pending delete for restored ancestor path=%q id=%q requested=%q", restorePath, entry.ID, path)
	}
	r.overlay.restoredDirs[restorePath] = time.Now().Add(RestoredDirTTL)
	r.overlay.mu.Unlock()

	r.view.mu.Lock()
	r.view.entries.Set(restorePath, entry)
	delete(r.view.lists, vfstypes.CleanVirtualPath(pathpkg.Dir(restorePath)))
	r.view.mu.Unlock()
}

// CancelDeletedFile drops a pending file delete when the file is recreated
// locally before the remote delete runs.
func (r Visibility) CancelDeletedFile(path string) {
	path = vfstypes.CleanVirtualPath(path)
	r.overlay.mu.Lock()
	entry, ok := r.overlay.deleted[path]
	if ok && !entry.IsDir {
		r.overlay.removeDeleted(path)
		r.deleteState.clearFailureLocked(path)
		if r.deleteState.unscheduleLocked(path) {
			logging.L.Infof("[VFS] canceled pending delete for recreated file path=%q id=%q", path, entry.ID)
		}
	}
	r.overlay.mu.Unlock()
}

// IsUnavailable reports whether the path is hidden by any overlay facet.
func (r Visibility) IsUnavailable(path string) bool {
	path = vfstypes.CleanVirtualPath(path)
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	return r.overlay.unavailableLocked(path)
}

// IsDeleted reports whether the path itself is deleted (or under a deleted
// directory).
func (r Visibility) IsDeleted(path string) bool {
	path = vfstypes.CleanVirtualPath(path)
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	return r.overlay.isDeletedPath(path)
}

// IsUnderRestoredDir reports whether path is under a recently restored
// directory.
func (r Visibility) IsUnderRestoredDir(path string) bool {
	path = vfstypes.CleanVirtualPath(path)
	now := time.Now()
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	for restoredPath, expires := range r.overlay.restoredDirs {
		if now.After(expires) {
			delete(r.overlay.restoredDirs, restoredPath)
			continue
		}
		if path == restoredPath || vfstypes.IsPathUnder(path, restoredPath) {
			return true
		}
	}
	return false
}

// FilterDeleted drops every entry hidden by the overlay from a listing
// snapshot (the input slice is not mutated).
func (r Visibility) FilterDeleted(parentPath string, entries []drive.Entry) []drive.Entry {
	entries = CloneEntries(entries)
	filtered := entries[:0]
	for _, entry := range entries {
		if r.IsUnavailable(vfstypes.JoinVirtualPath(parentPath, entry.Name)) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// LocalChildren merges locally cached entries (pending writes, freshly
// committed mutations) that a remote listing does not contain yet.
func (r Visibility) LocalChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	parentPath = vfstypes.CleanVirtualPath(parentPath)
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.Name] = true
	}
	var local []struct {
		path  string
		entry drive.Entry
	}
	r.view.entries.Range(func(path string, entry drive.Entry) bool {
		if path == "/" || pathpkg.Dir(path) != parentPath || seen[entry.Name] {
			return true
		}
		local = append(local, struct {
			path  string
			entry drive.Entry
		}{path: path, entry: entry})
		return true
	})
	for _, item := range local {
		if seen[item.entry.Name] || r.IsUnavailable(item.path) {
			continue
		}
		entries = append(entries, item.entry)
		seen[item.entry.Name] = true
	}
	return entries
}

// AddRenameOverlay records a rename shadow: while the remote rename is in
// flight, listings hide the old path. A recursive overlay prunes nested
// overlays under it.
func (r Visibility) AddRenameOverlay(oldPath, newPath, entryID string, recursive bool) {
	oldPath = vfstypes.CleanVirtualPath(oldPath)
	newPath = vfstypes.CleanVirtualPath(newPath)
	if r.lister != nil {
		r.lister.SuppressDirPrefetch(oldPath)
	}
	r.overlay.mu.Lock()
	r.overlay.setRenameOverlay(overlayOp{oldPath: oldPath, newPath: newPath, entryID: entryID, isDir: recursive})
	if recursive {
		for key, op := range r.overlay.renameOverlays {
			if key != oldPath && vfstypes.IsPathUnder(op.oldPath, oldPath) {
				r.overlay.removeRenameOverlay(key)
			}
		}
	}
	r.overlay.mu.Unlock()
}

// IsHidden reports whether path is shadowed by a rename overlay.
func (r Visibility) IsHidden(path string) bool {
	path = vfstypes.CleanVirtualPath(path)
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	return r.overlay.isRenameHiddenPath(path)
}

// UpdateRenameOverlay reconciles rename overlays against a freshly listed
// parent: an overlay whose old name is gone and new name is visible is
// removed, and one that outlived RenameShadowTTL is dropped.
//
// Matching is by name on both ends. The identity recorded with the shadow
// cannot be relied on here: a backend that derives ids from the location
// reports a new id for the moved object, and a stale listing still carries
// the old name either way. A different object taking over the old path is
// retired by the commit that writes it (see RetireRenameShadow), and the TTL
// bounds every other way the shadow could linger.
func (r Visibility) UpdateRenameOverlay(parentPath string, entries []drive.Entry) {
	parentPath = vfstypes.CleanVirtualPath(parentPath)
	now := time.Now()
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	for key, op := range r.overlay.renameOverlays {
		if op.expired(now) {
			r.overlay.removeRenameOverlay(key)
			continue
		}
		if pathpkg.Dir(op.oldPath) == parentPath {
			op.oldGone = !entryListHasPath(entries, pathpkg.Base(op.oldPath), "")
		}
		if pathpkg.Dir(op.newPath) == parentPath {
			op.newSeen = entryListHasPath(entries, pathpkg.Base(op.newPath), "")
		}
		if op.oldGone && op.newSeen {
			r.overlay.removeRenameOverlay(key)
			continue
		}
		r.overlay.setRenameOverlay(op)
	}
}

// RetireRenameShadow drops a rename shadow whose old path now belongs to a
// different object: a new file or directory committed at that path must be
// visible even while a stale backend listing still carries the old name.
func (r Visibility) RetireRenameShadow(path, entryID string) {
	path = vfstypes.CleanVirtualPath(path)
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	op, ok := r.overlay.renameOverlays[path]
	if !ok || op.entryID == entryID {
		return
	}
	r.overlay.removeRenameOverlay(path)
}

// SetCopyHidden hides names under dir until their deadlines (directory copy
// in progress); an empty set clears the hiding.
func (r Visibility) SetCopyHidden(dir string, names map[string]time.Time) {
	dir = vfstypes.CleanVirtualPath(dir)
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	if len(names) == 0 {
		delete(r.overlay.copyHiddenChildren, dir)
		return
	}
	r.overlay.copyHiddenChildren[dir] = names
}

// UnhideCopyChild stops hiding one child under parentPath.
func (r Visibility) UnhideCopyChild(parentPath, name string) {
	parentPath = vfstypes.CleanVirtualPath(parentPath)
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	if names := r.overlay.copyHiddenChildren[parentPath]; names != nil {
		delete(names, name)
		if len(names) == 0 {
			delete(r.overlay.copyHiddenChildren, parentPath)
		}
	}
}

// IsCopyHidden reports whether path is currently copy-hidden.
func (r Visibility) IsCopyHidden(path string) bool {
	path = vfstypes.CleanVirtualPath(path)
	parentPath := pathpkg.Dir(path)
	name := pathpkg.Base(path)
	now := time.Now()
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	names := r.overlay.copyHiddenChildren[parentPath]
	if len(names) == 0 {
		delete(r.overlay.copyHiddenChildren, parentPath)
		return false
	}
	expires, ok := names[name]
	if !ok {
		return false
	}
	if now.After(expires) {
		delete(names, name)
		if len(names) == 0 {
			delete(r.overlay.copyHiddenChildren, parentPath)
		}
		return false
	}
	return true
}

// --- delete-executor operations (idelete.OverlayOps surface) ---

// BeginDelete reports whether path's pending delete may run: it must be the
// current deleted overlay entry and not inside a taken-over directory.
func (r Visibility) BeginDelete(path string, entryID string) bool {
	path = vfstypes.CleanVirtualPath(path)
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	current, ok := r.overlay.deleted[path]
	if !ok || current.ID != entryID {
		return false
	}
	for takeover := range r.deleteState.takeovers {
		if takeover != path && vfstypes.IsPathUnder(path, takeover) {
			return false
		}
	}
	return true
}

// MarkDeleteActive records path as an in-flight delete, clearing any prior
// failure.
func (r Visibility) MarkDeleteActive(path string, entry drive.Entry) {
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	r.deleteState.active[path] = entry
	r.deleteState.clearFailureLocked(path)
	r.deleteState.notifyChangedLocked()
}

// MarkDeleteFailed records a failed delete for path.
func (r Visibility) MarkDeleteFailed(path string, err error) {
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	delete(r.deleteState.active, path)
	if err != nil {
		r.deleteState.failures[path] = err.Error()
	}
	r.deleteState.notifyChangedLocked()
}

// MarkDeleteComplete folds a finished remote delete back into the view: the
// overlay entry and its restore marker are removed, the delete-task state is
// cleared, and the parent list cache is invalidated so the next listing
// refetches the gone path.
func (r Visibility) MarkDeleteComplete(path string, entry drive.Entry) {
	r.overlay.mu.Lock()
	delete(r.deleteState.active, path)
	r.deleteState.clearFailureLocked(path)
	r.deleteState.clearTakeoverLocked(path)
	r.overlay.removeDeleted(path)
	delete(r.overlay.restoredDirs, path)
	r.deleteState.notifyChangedLocked()
	r.overlay.mu.Unlock()

	r.view.mu.Lock()
	delete(r.view.lists, vfstypes.CleanVirtualPath(pathpkg.Dir(path)))
	r.view.mu.Unlock()
}

// CancelDelete cancels a pending delete outright: the overlay entry and all
// delete-task state for the path are dropped.
func (r Visibility) CancelDelete(path string) {
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	delete(r.deleteState.active, path)
	r.deleteState.clearFailureLocked(path)
	r.deleteState.clearTakeoverLocked(path)
	r.deleteState.scheduler.Cancel(path)
	r.overlay.removeDeleted(path)
	r.deleteState.notifyChangedLocked()
}

// --- delete scheduler operations ---

// ScheduleDelete arms (or moves) a delayed remote delete for path, clearing
// any prior failure recording.
func (r Visibility) ScheduleDelete(path string, delay time.Duration, fire func()) {
	r.overlay.mu.Lock()
	r.deleteState.clearFailureLocked(path)
	r.overlay.mu.Unlock()
	r.deleteState.scheduler.Schedule(path, delay, fire)
}

// TakeoverDirectory cancels every pending delete inside dir (children only,
// not dir itself) and marks the directory as taken over so its children's
// deletes never begin until the takeover is lifted by a restore/complete.
func (r Visibility) TakeoverDirectory(dir string) {
	dir = vfstypes.CleanVirtualPath(dir)
	r.overlay.mu.Lock()
	r.deleteState.takeovers[dir] = struct{}{}
	r.deleteState.notifyChangedLocked()
	r.overlay.mu.Unlock()

	removed := []string{}
	for path := range r.deleteState.scheduler.Keys() {
		if path != dir && vfstypes.IsPathUnder(path, dir) {
			removed = append(removed, path)
		}
	}
	r.deleteState.scheduler.CancelUnder(dir)
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	for _, path := range removed {
		r.overlay.removeDeleted(path)
		r.deleteState.clearFailureLocked(path)
	}
}

// CancelChildren cancels every pending delete inside dir (directory removal
// path; TakeoverDirectory semantics).
func (r Visibility) CancelChildren(dir string) {
	r.TakeoverDirectory(dir)
}
