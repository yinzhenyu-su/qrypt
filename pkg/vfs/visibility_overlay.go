package vfs

import (
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"path/filepath"
	"sync"
	"time"
)

type overlayState struct {
	mu                 *sync.Mutex
	deleted            map[string]drive.Entry
	renameOverlays     map[string]overlayOp
	restoredDirs       map[string]time.Time
	copyHiddenChildren map[string]map[string]time.Time
}

type deleteTaskState struct {
	mu       *sync.Mutex
	timers   map[string]*time.Timer
	active   map[string]drive.Entry
	failures map[string]string
}

func newDeleteStates() (*overlayState, *deleteTaskState) {
	mu := &sync.Mutex{}
	return &overlayState{
			mu:                 mu,
			deleted:            map[string]drive.Entry{},
			renameOverlays:     map[string]overlayOp{},
			restoredDirs:       map[string]time.Time{},
			copyHiddenChildren: map[string]map[string]time.Time{},
		}, &deleteTaskState{
			mu:       mu,
			timers:   map[string]*time.Timer{},
			active:   map[string]drive.Entry{},
			failures: map[string]string{},
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

func (r vfsVisibilityRuntime) MarkDeleted(path string, entry drive.Entry) {
	r.v.view.overlay.mu.Lock()
	r.v.view.overlay.deleted[path] = entry
	delete(r.v.delete.tasks.failures, path)
	delete(r.v.view.overlay.renameOverlays, path)
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
	delete(r.v.view.overlay.deleted, path)
	delete(r.v.delete.tasks.failures, path)
	if timer := r.v.delete.tasks.timers[path]; timer != nil {
		timer.Stop()
		delete(r.v.delete.tasks.timers, path)
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
	var restorePath string
	var entry drive.Entry
	for deletedPath, deletedEntry := range r.v.view.overlay.deleted {
		if deletedEntry.IsDir && (path == deletedPath || isPathUnder(path, deletedPath)) {
			if restorePath == "" || len(deletedPath) > len(restorePath) {
				restorePath = deletedPath
				entry = deletedEntry
			}
		}
	}
	if restorePath == "" {
		r.v.view.overlay.mu.Unlock()
		return
	}
	delete(r.v.view.overlay.deleted, restorePath)
	delete(r.v.delete.tasks.failures, restorePath)
	if timer := r.v.delete.tasks.timers[restorePath]; timer != nil {
		timer.Stop()
		delete(r.v.delete.tasks.timers, restorePath)
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
		delete(r.v.view.overlay.deleted, path)
		delete(r.v.delete.tasks.failures, path)
		if timer := r.v.delete.tasks.timers[path]; timer != nil {
			timer.Stop()
			delete(r.v.delete.tasks.timers, path)
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
	for deletedPath, entry := range r.v.view.overlay.deleted {
		if path == deletedPath || (entry.IsDir && isPathUnder(path, deletedPath)) {
			return true
		}
	}
	return false
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
	r.v.view.overlay.renameOverlays[oldPath] = overlayOp{oldPath: oldPath, newPath: newPath, entryID: entryID, isDir: recursive}
	if recursive {
		for key, op := range r.v.view.overlay.renameOverlays {
			if key != oldPath && isPathUnder(op.oldPath, oldPath) {
				delete(r.v.view.overlay.renameOverlays, key)
			}
		}
	}
	r.v.view.overlay.mu.Unlock()
}

func (r vfsVisibilityRuntime) IsHidden(path string) bool {
	path = cleanVirtual(path)
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	for _, op := range r.v.view.overlay.renameOverlays {
		if path == op.oldPath || (op.isDir && isPathUnder(path, op.oldPath)) {
			return true
		}
	}
	return false
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
			delete(r.v.view.overlay.renameOverlays, key)
			continue
		}
		r.v.view.overlay.renameOverlays[key] = op
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
// after shutdown. Used by deleteState.Close and the delete scheduler.
func (s *deleteTaskState) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for path, timer := range s.timers {
		timer.Stop()
		delete(s.timers, path)
	}
}
