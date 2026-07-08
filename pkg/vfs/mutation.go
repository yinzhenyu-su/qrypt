package vfs

import (
	"context"
	"path/filepath"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (v *VFS) markDeleted(path string, entry drive.Entry) {
	v.deleteMu.Lock()
	v.deleted[path] = entry
	delete(v.overlayOps, path)
	delete(v.restoredDirs, path)
	v.deleteMu.Unlock()

	v.mu.Lock()
	delete(v.entries, path)
	if entry.IsDir {
		for cachedPath := range v.entries {
			if isPathUnder(cachedPath, path) {
				delete(v.entries, cachedPath)
			}
		}
		for cachedPath := range v.lists {
			if cachedPath == path || isPathUnder(cachedPath, path) {
				delete(v.lists, cachedPath)
			}
		}
	}
	v.mu.Unlock()
}

func (v *VFS) restoreDeletedPath(path string) (drive.Entry, bool) {
	path = cleanVirtual(path)
	v.deleteMu.Lock()
	entry, ok := v.deleted[path]
	if !ok {
		v.deleteMu.Unlock()
		return drive.Entry{}, false
	}
	delete(v.deleted, path)
	if timer := v.deleteTimers[path]; timer != nil {
		timer.Stop()
		delete(v.deleteTimers, path)
		logging.L.Infof("[VFS] canceled pending delete for restored path=%q id=%q", path, entry.ID)
	}
	if entry.IsDir {
		v.restoredDirs[path] = time.Now().Add(restoredDirTTL)
	}
	v.deleteMu.Unlock()

	v.mu.Lock()
	v.entries[path] = entry
	v.invalidateListLocked(filepath.Dir(path))
	v.mu.Unlock()
	return entry, true
}

func (v *VFS) restoreDeletedAncestor(path string) {
	path = cleanVirtual(path)
	v.deleteMu.Lock()
	var restorePath string
	var entry drive.Entry
	for deletedPath, deletedEntry := range v.deleted {
		if deletedEntry.IsDir && (path == deletedPath || isPathUnder(path, deletedPath)) {
			if restorePath == "" || len(deletedPath) > len(restorePath) {
				restorePath = deletedPath
				entry = deletedEntry
			}
		}
	}
	if restorePath == "" {
		v.deleteMu.Unlock()
		return
	}
	delete(v.deleted, restorePath)
	if timer := v.deleteTimers[restorePath]; timer != nil {
		timer.Stop()
		delete(v.deleteTimers, restorePath)
		logging.L.Infof("[VFS] canceled pending delete for restored ancestor path=%q id=%q requested=%q", restorePath, entry.ID, path)
	}
	v.restoredDirs[restorePath] = time.Now().Add(restoredDirTTL)
	v.deleteMu.Unlock()

	v.mu.Lock()
	v.entries[restorePath] = entry
	v.invalidateListLocked(filepath.Dir(restorePath))
	v.mu.Unlock()
}

func (v *VFS) cancelDeletedFile(path string) {
	path = cleanVirtual(path)
	v.deleteMu.Lock()
	entry, ok := v.deleted[path]
	if ok && !entry.IsDir {
		delete(v.deleted, path)
		if timer := v.deleteTimers[path]; timer != nil {
			timer.Stop()
			delete(v.deleteTimers, path)
			logging.L.Infof("[VFS] canceled pending delete for recreated file path=%q id=%q", path, entry.ID)
		}
	}
	v.deleteMu.Unlock()
}

func (v *VFS) addOverlay(oldPath, newPath, entryID string, recursive bool) {
	oldPath = cleanVirtual(oldPath)
	newPath = cleanVirtual(newPath)
	v.deleteMu.Lock()
	v.overlayOps[oldPath] = overlayOp{oldPath: oldPath, newPath: newPath, entryID: entryID, isDir: recursive}
	if recursive {
		for key, op := range v.overlayOps {
			if key != oldPath && isPathUnder(op.oldPath, oldPath) {
				delete(v.overlayOps, key)
			}
		}
	}
	v.deleteMu.Unlock()
}

func (v *VFS) scheduleDelete(path string, entry drive.Entry) {
	if v.deleteDelay <= 0 {
		logging.L.Infof("[VFS] delete remote now path=%q id=%q dir=%t", path, entry.ID, entry.IsDir)
		v.deleteRemote(context.Background(), path, entry)
		return
	}
	v.deleteMu.Lock()
	if timer := v.deleteTimers[path]; timer != nil {
		timer.Stop()
	}
	v.deleteTimers[path] = time.AfterFunc(v.deleteDelay, func() {
		v.deleteMu.Lock()
		delete(v.deleteTimers, path)
		v.deleteMu.Unlock()
		logging.L.Infof("[VFS] delete timer fired path=%q id=%q dir=%t", path, entry.ID, entry.IsDir)
		v.deleteRemote(context.Background(), path, entry)
	})
	v.deleteMu.Unlock()
}

func (v *VFS) cancelChildDeletes(dir string) {
	dir = cleanVirtual(dir)
	v.deleteMu.Lock()
	for path, timer := range v.deleteTimers {
		if isPathUnder(path, dir) {
			timer.Stop()
			delete(v.deleteTimers, path)
			delete(v.deleted, path)
		}
	}
	v.deleteMu.Unlock()
}

func (v *VFS) deleteRemote(ctx context.Context, path string, entry drive.Entry) {
	v.deleteMu.Lock()
	current, ok := v.deleted[path]
	if !ok || current.ID != entry.ID {
		v.deleteMu.Unlock()
		logging.L.Infof("[VFS] delete remote skipped path=%q id=%q current_ok=%t current_id=%q", path, entry.ID, ok, current.ID)
		return
	}
	v.deleteMu.Unlock()
	if err := v.writer.Remove(ctx, entry); err != nil {
		logging.L.Warnf("[VFS] delete remote failed path=%q id=%q dir=%t err=%v", path, entry.ID, entry.IsDir, err)
		return
	}
	logging.L.Infof("[VFS] delete remote complete path=%q id=%q dir=%t", path, entry.ID, entry.IsDir)
	v.deleteMu.Lock()
	delete(v.deleted, path)
	delete(v.restoredDirs, path)
	v.deleteMu.Unlock()

	v.mu.Lock()
	v.invalidateListLocked(filepath.Dir(path))
	v.mu.Unlock()

	_ = v.cache.RemovePending(path)
}

func (v *VFS) stopDeleteTimers() {
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	for path, timer := range v.deleteTimers {
		timer.Stop()
		delete(v.deleteTimers, path)
	}
}

func (v *VFS) isUnavailable(path string) bool {
	return v.isDeleted(path) || v.isHidden(path) || v.isCopyHidden(path)
}

func (v *VFS) isDeleted(path string) bool {
	path = cleanVirtual(path)
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	for deletedPath, entry := range v.deleted {
		if path == deletedPath || (entry.IsDir && isPathUnder(path, deletedPath)) {
			return true
		}
	}
	return false
}

func (v *VFS) isHidden(path string) bool {
	path = cleanVirtual(path)
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	for _, op := range v.overlayOps {
		if path == op.oldPath || (op.isDir && isPathUnder(path, op.oldPath)) {
			return true
		}
	}
	return false
}

func (v *VFS) isUnderRestoredDir(path string) bool {
	path = cleanVirtual(path)
	now := time.Now()
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	for restoredPath, expires := range v.restoredDirs {
		if now.After(expires) {
			delete(v.restoredDirs, restoredPath)
			continue
		}
		if path == restoredPath || isPathUnder(path, restoredPath) {
			return true
		}
	}
	return false
}

func (v *VFS) setCopyHidden(dir string, names map[string]time.Time) {
	dir = cleanVirtual(dir)
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	if len(names) == 0 {
		delete(v.copyHidden, dir)
		return
	}
	v.copyHidden[dir] = names
}

func (v *VFS) unhideCopyChild(parentPath, name string) {
	parentPath = cleanVirtual(parentPath)
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	if names := v.copyHidden[parentPath]; names != nil {
		delete(names, name)
		if len(names) == 0 {
			delete(v.copyHidden, parentPath)
		}
	}
}

func (v *VFS) isCopyHidden(path string) bool {
	path = cleanVirtual(path)
	parentPath := filepath.Dir(path)
	name := filepath.Base(path)
	now := time.Now()
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	names := v.copyHidden[parentPath]
	if len(names) == 0 {
		delete(v.copyHidden, parentPath)
		return false
	}
	expires, ok := names[name]
	if !ok {
		return false
	}
	if now.After(expires) {
		delete(names, name)
		if len(names) == 0 {
			delete(v.copyHidden, parentPath)
		}
		return false
	}
	return true
}

func (v *VFS) updateOverlay(parentPath string, entries []drive.Entry) {
	parentPath = cleanVirtual(parentPath)
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	for key, op := range v.overlayOps {
		if filepath.Dir(op.oldPath) == parentPath {
			op.oldGone = !entryListHasPath(entries, filepath.Base(op.oldPath), op.entryID)
		}
		if filepath.Dir(op.newPath) == parentPath {
			op.newSeen = entryListHasPath(entries, filepath.Base(op.newPath), op.entryID)
		}
		if op.oldGone && op.newSeen {
			delete(v.overlayOps, key)
			continue
		}
		v.overlayOps[key] = op
	}
}

func (v *VFS) filterDeleted(parentPath string, entries []drive.Entry) []drive.Entry {
	entries = cloneEntries(entries)
	filtered := entries[:0]
	for _, entry := range entries {
		if v.isUnavailable(joinVirtual(parentPath, entry.Name)) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func (v *VFS) localChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	parentPath = cleanVirtual(parentPath)
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.Name] = true
	}
	var local []struct {
		path  string
		entry drive.Entry
	}
	v.mu.RLock()
	for path, entry := range v.entries {
		if path == "/" || filepath.Dir(path) != parentPath || seen[entry.Name] {
			continue
		}
		local = append(local, struct {
			path  string
			entry drive.Entry
		}{path: path, entry: entry})
	}
	v.mu.RUnlock()
	for _, item := range local {
		if seen[item.entry.Name] || v.isUnavailable(item.path) {
			continue
		}
		entries = append(entries, item.entry)
		seen[item.entry.Name] = true
	}
	return entries
}
