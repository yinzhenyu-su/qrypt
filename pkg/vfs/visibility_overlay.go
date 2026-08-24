package vfs

import (
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs/scheduler"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type overlayState struct {
	mu                 *sync.Mutex
	deleted            map[string]drive.Entry
	renameOverlays     map[string]overlayOp
	restoredDirs       map[string]time.Time
	copyHiddenChildren map[string]map[string]time.Time
	// deletedDirs indexes the directory entries of the deleted map so
	// IsDeleted can walk the O(depth) ancestor chain instead of scanning
	// every overlay entry. renameHiddenDirs does the same for recursive
	// rename overlays (keyed by oldPath). Both are maintained by the
	// set/remove helpers below, always with mu held.
	deletedDirs      map[string]struct{}
	renameHiddenDirs map[string]struct{}
}

type deleteTaskState struct {
	mu        *sync.Mutex
	scheduler scheduler.KeyedScheduler
	active    map[string]drive.Entry
	failures  map[string]string
	takeovers map[string]struct{}
	changed   chan struct{}
}

func newDeleteStates() (*overlayState, *deleteTaskState) {
	mu := &sync.Mutex{}
	return &overlayState{
		mu:                 mu,
		deleted:            map[string]drive.Entry{},
		renameOverlays:     map[string]overlayOp{},
		restoredDirs:       map[string]time.Time{},
		copyHiddenChildren: map[string]map[string]time.Time{},
		deletedDirs:        map[string]struct{}{},
		renameHiddenDirs:   map[string]struct{}{},
	}, &deleteTaskState{
		mu:        mu,
		scheduler: scheduler.NewTimeKeyedScheduler(),
		active:    map[string]drive.Entry{},
		failures:  map[string]string{},
		takeovers: map[string]struct{}{},
		changed:   make(chan struct{}),
	}
}

func newVisibilityOverlayState() *overlayState {
	overlay, _ := newDeleteStates()
	return overlay
}

type vfsVisibilityRuntime struct {
	v *VFS
}

func newVFSVisibilityRuntime(v *VFS) vfsVisibilityRuntime {
	return vfsVisibilityRuntime{v: v}
}

// --- overlay map maintenance helpers (callers must hold overlay.mu) ---

func (o *overlayState) setDeleted(path string, entry drive.Entry) {
	o.deleted[path] = entry
	if entry.IsDir {
		o.deletedDirs[path] = struct{}{}
	} else {
		delete(o.deletedDirs, path)
	}
}

func (o *overlayState) removeDeleted(path string) {
	delete(o.deleted, path)
	delete(o.deletedDirs, path)
}

func (o *overlayState) setRenameOverlay(op overlayOp) {
	o.renameOverlays[op.oldPath] = op
	if op.isDir {
		o.renameHiddenDirs[op.oldPath] = struct{}{}
	} else {
		delete(o.renameHiddenDirs, op.oldPath)
	}
}

func (o *overlayState) removeRenameOverlay(path string) {
	delete(o.renameOverlays, path)
	delete(o.renameHiddenDirs, path)
}

// isDeletedPath reports whether path itself is deleted or lives under a
// deleted directory. Exact hits are O(1); directory shadowing walks the
// ancestor chain (O(depth)) instead of scanning every overlay entry.
func (o *overlayState) isDeletedPath(path string) bool {
	if _, ok := o.deleted[path]; ok {
		return true
	}
	for dir := parentVirtualPath(path); ; dir = parentVirtualPath(dir) {
		if _, ok := o.deletedDirs[dir]; ok {
			return true
		}
		if dir == "/" || dir == "." {
			return false
		}
	}
}

func (o *overlayState) isRenameHiddenPath(path string) bool {
	if _, ok := o.renameOverlays[path]; ok {
		return true
	}
	for dir := parentVirtualPath(path); ; dir = parentVirtualPath(dir) {
		if _, ok := o.renameHiddenDirs[dir]; ok {
			return true
		}
		if dir == "/" || dir == "." {
			return false
		}
	}
}

// deepestDeletedAncestor returns the deepest deleted directory at or above
// path (matching the longest-match semantics of the former full scan).
func (o *overlayState) deepestDeletedAncestor(path string) (string, drive.Entry, bool) {
	for dir := path; ; dir = parentVirtualPath(dir) {
		if entry, ok := o.deleted[dir]; ok && entry.IsDir {
			return dir, entry, true
		}
		if dir == "/" || dir == "." {
			return "", drive.Entry{}, false
		}
	}
}

// parentVirtualPath returns the parent of a slash-absolute virtual path
// without allocation (callers guarantee cleanVirtual-normalized input).
func parentVirtualPath(path string) string {
	if path == "/" {
		return "/"
	}
	if i := strings.LastIndexByte(path, '/'); i > 0 {
		return path[:i]
	}
	return "/"
}

func (r vfsVisibilityRuntime) MarkDeleted(path string, entry drive.Entry) {
	r.v.view.overlay.mu.Lock()
	r.v.view.overlay.setDeleted(path, entry)
	delete(r.v.deletes.tasks.failures, path)
	r.v.view.overlay.removeRenameOverlay(path)
	delete(r.v.view.overlay.restoredDirs, path)
	r.v.view.overlay.mu.Unlock()

	r.v.view.mu.Lock()
	r.v.view.entries.Delete(path)
	if entry.IsDir {
		r.v.view.entries.DeleteUnder(path)
		for cachedPath := range r.v.view.lists {
			if cachedPath == path || isPathUnder(cachedPath, path) {
				delete(r.v.view.lists, cachedPath)
			}
		}
	}
	r.v.view.mu.Unlock()
}

func (r vfsVisibilityRuntime) RestoreDeletedPath(path string) (drive.Entry, bool) {
	path = cleanVirtual(path)
	r.v.view.overlay.mu.Lock()
	entry, ok := r.v.view.overlay.deleted[path]
	if !ok {
		r.v.view.overlay.mu.Unlock()
		return drive.Entry{}, false
	}
	delete(r.v.deletes.tasks.failures, path)
	r.v.deletes.tasks.clearTakeoverLocked(path)
	r.v.view.overlay.removeDeleted(path)
	if _, ok := r.v.deletes.tasks.scheduler.Keys()[path]; ok {
		r.v.deletes.tasks.scheduler.Cancel(path)
		logging.L.Infof("[VFS] canceled pending delete for restored path=%q id=%q", path, entry.ID)
	}
	if entry.IsDir {
		r.v.view.overlay.restoredDirs[path] = time.Now().Add(restoredDirTTL)
	}
	r.v.view.overlay.mu.Unlock()

	r.v.view.mu.Lock()
	r.v.view.entries.Set(path, entry)
	r.v.invalidateListLocked(filepath.Dir(path))
	r.v.view.mu.Unlock()
	return entry, true
}

func (r vfsVisibilityRuntime) RestoreDeletedAncestor(path string) {
	path = cleanVirtual(path)
	r.v.view.overlay.mu.Lock()
	restorePath, entry, ok := r.v.view.overlay.deepestDeletedAncestor(path)
	if !ok {
		r.v.view.overlay.mu.Unlock()
		return
	}
	r.v.view.overlay.removeDeleted(restorePath)
	delete(r.v.deletes.tasks.failures, restorePath)
	r.v.deletes.tasks.clearTakeoverLocked(restorePath)
	if _, ok := r.v.deletes.tasks.scheduler.Keys()[restorePath]; ok {
		r.v.deletes.tasks.scheduler.Cancel(restorePath)
		logging.L.Infof("[VFS] canceled pending delete for restored ancestor path=%q id=%q requested=%q", restorePath, entry.ID, path)
	}
	r.v.view.overlay.restoredDirs[restorePath] = time.Now().Add(restoredDirTTL)
	r.v.view.overlay.mu.Unlock()

	r.v.view.mu.Lock()
	r.v.view.entries.Set(restorePath, entry)
	r.v.invalidateListLocked(filepath.Dir(restorePath))
	r.v.view.mu.Unlock()
}

func (r vfsVisibilityRuntime) CancelDeletedFile(path string) {
	path = cleanVirtual(path)
	r.v.view.overlay.mu.Lock()
	entry, ok := r.v.view.overlay.deleted[path]
	if ok && !entry.IsDir {
		r.v.view.overlay.removeDeleted(path)
		delete(r.v.deletes.tasks.failures, path)
		if _, ok := r.v.deletes.tasks.scheduler.Keys()[path]; ok {
			r.v.deletes.tasks.scheduler.Cancel(path)
			logging.L.Infof("[VFS] canceled pending delete for recreated file path=%q id=%q", path, entry.ID)
		}
	}
	r.v.view.overlay.mu.Unlock()
}

func (r vfsVisibilityRuntime) IsUnavailable(path string) bool {
	return r.IsDeleted(path) || r.IsHidden(path) || r.IsCopyHidden(path)
}

func (r vfsVisibilityRuntime) IsDeleted(path string) bool {
	path = cleanVirtual(path)
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	return r.v.view.overlay.isDeletedPath(path)
}

func (r vfsVisibilityRuntime) IsUnderRestoredDir(path string) bool {
	path = cleanVirtual(path)
	now := time.Now()
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	for restoredPath, expires := range r.v.view.overlay.restoredDirs {
		if now.After(expires) {
			delete(r.v.view.overlay.restoredDirs, restoredPath)
			continue
		}
		if path == restoredPath || isPathUnder(path, restoredPath) {
			return true
		}
	}
	return false
}

func (r vfsVisibilityRuntime) FilterDeleted(parentPath string, entries []drive.Entry) []drive.Entry {
	entries = cloneEntries(entries)
	filtered := entries[:0]
	for _, entry := range entries {
		if r.IsUnavailable(joinVirtual(parentPath, entry.Name)) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func (r vfsVisibilityRuntime) LocalChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	parentPath = cleanVirtual(parentPath)
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.Name] = true
	}
	var local []struct {
		path  string
		entry drive.Entry
	}
	r.v.view.entries.Range(func(path string, entry drive.Entry) bool {
		if path == "/" || filepath.Dir(path) != parentPath || seen[entry.Name] {
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

func (r vfsVisibilityRuntime) AddRenameOverlay(oldPath, newPath, entryID string, recursive bool) {
	oldPath = cleanVirtual(oldPath)
	newPath = cleanVirtual(newPath)
	r.v.suppressDirPrefetch(oldPath)
	r.v.view.overlay.mu.Lock()
	r.v.view.overlay.setRenameOverlay(overlayOp{oldPath: oldPath, newPath: newPath, entryID: entryID, isDir: recursive})
	if recursive {
		for key, op := range r.v.view.overlay.renameOverlays {
			if key != oldPath && isPathUnder(op.oldPath, oldPath) {
				r.v.view.overlay.removeRenameOverlay(key)
			}
		}
	}
	r.v.view.overlay.mu.Unlock()
}

func (r vfsVisibilityRuntime) IsHidden(path string) bool {
	path = cleanVirtual(path)
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	return r.v.view.overlay.isRenameHiddenPath(path)
}

func (r vfsVisibilityRuntime) UpdateRenameOverlay(parentPath string, entries []drive.Entry) {
	parentPath = cleanVirtual(parentPath)
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	for key, op := range r.v.view.overlay.renameOverlays {
		if filepath.Dir(op.oldPath) == parentPath {
			op.oldGone = !entryListHasPath(entries, filepath.Base(op.oldPath), op.entryID)
		}
		if filepath.Dir(op.newPath) == parentPath {
			op.newSeen = entryListHasPath(entries, filepath.Base(op.newPath), op.entryID)
		}
		if op.oldGone && op.newSeen {
			r.v.view.overlay.removeRenameOverlay(key)
			continue
		}
		r.v.view.overlay.setRenameOverlay(op)
	}
}

func (r vfsVisibilityRuntime) SetCopyHidden(dir string, names map[string]time.Time) {
	dir = cleanVirtual(dir)
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	if len(names) == 0 {
		delete(r.v.view.overlay.copyHiddenChildren, dir)
		return
	}
	r.v.view.overlay.copyHiddenChildren[dir] = names
}

func (r vfsVisibilityRuntime) UnhideCopyChild(parentPath, name string) {
	parentPath = cleanVirtual(parentPath)
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	if names := r.v.view.overlay.copyHiddenChildren[parentPath]; names != nil {
		delete(names, name)
		if len(names) == 0 {
			delete(r.v.view.overlay.copyHiddenChildren, parentPath)
		}
	}
}

func (r vfsVisibilityRuntime) IsCopyHidden(path string) bool {
	path = cleanVirtual(path)
	parentPath := filepath.Dir(path)
	name := filepath.Base(path)
	now := time.Now()
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	names := r.v.view.overlay.copyHiddenChildren[parentPath]
	if len(names) == 0 {
		delete(r.v.view.overlay.copyHiddenChildren, parentPath)
		return false
	}
	expires, ok := names[name]
	if !ok {
		return false
	}
	if now.After(expires) {
		delete(names, name)
		if len(names) == 0 {
			delete(r.v.view.overlay.copyHiddenChildren, parentPath)
		}
		return false
	}
	return true
}

func (v *VFS) setCopyHidden(dir string, names map[string]time.Time) {
	newVFSVisibilityRuntime(v).SetCopyHidden(dir, names)
}
func (v *VFS) unhideCopyChild(parentPath, name string) {
	newVFSVisibilityRuntime(v).UnhideCopyChild(parentPath, name)
}
func (v *VFS) isCopyHidden(path string) bool {
	return newVFSVisibilityRuntime(v).IsCopyHidden(path)
}

func (v *VFS) markDeleted(path string, entry drive.Entry) {
	newVFSVisibilityRuntime(v).MarkDeleted(path, entry)
}
func (v *VFS) restoreDeletedPath(path string) (drive.Entry, bool) {
	return newVFSVisibilityRuntime(v).RestoreDeletedPath(path)
}
func (v *VFS) restoreDeletedAncestor(path string) {
	newVFSVisibilityRuntime(v).RestoreDeletedAncestor(path)
}
func (v *VFS) cancelDeletedFile(path string) {
	newVFSVisibilityRuntime(v).CancelDeletedFile(path)
}
func (v *VFS) isUnavailable(path string) bool {
	return newVFSVisibilityRuntime(v).IsUnavailable(path)
}
func (v *VFS) isDeleted(path string) bool {
	return newVFSVisibilityRuntime(v).IsDeleted(path)
}
func (v *VFS) isUnderRestoredDir(path string) bool {
	return newVFSVisibilityRuntime(v).IsUnderRestoredDir(path)
}
func (v *VFS) filterDeleted(parentPath string, entries []drive.Entry) []drive.Entry {
	return newVFSVisibilityRuntime(v).FilterDeleted(parentPath, entries)
}
func (v *VFS) localChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	return newVFSVisibilityRuntime(v).LocalChildren(parentPath, entries)
}

func (v *VFS) addOverlay(oldPath, newPath, entryID string, recursive bool) {
	newVFSVisibilityRuntime(v).AddRenameOverlay(oldPath, newPath, entryID, recursive)
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

func (v *VFS) updateOverlay(parentPath string, entries []drive.Entry) {
	newVFSVisibilityRuntime(v).UpdateRenameOverlay(parentPath, entries)
}

// stopAll stops every pending delete timer so no delayed delete fires
// after shutdown. Used by DeleteService.Close and the delete scheduler.
func (s *deleteTaskState) stopAll() {
	s.scheduler.CancelAll()
}

func (s *deleteTaskState) notifyChangedLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *deleteTaskState) clearTakeoverLocked(path string) {
	changed := false
	for takeover := range s.takeovers {
		if takeover == path || isPathUnder(takeover, path) {
			delete(s.takeovers, takeover)
			changed = true
		}
	}
	if changed {
		s.notifyChangedLocked()
	}
}
