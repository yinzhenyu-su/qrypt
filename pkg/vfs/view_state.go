package vfs

import (
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type viewState struct {
	mu           sync.RWMutex
	entries      map[string]drive.Entry
	lists        map[string]listCacheEntry
	localDirs    map[string]time.Time
	localModTime map[string]time.Time
	overlay      *overlayState
}

func newViewState(rootID string, now time.Time) *viewState {
	view := &viewState{
		entries:      map[string]drive.Entry{},
		lists:        map[string]listCacheEntry{},
		localDirs:    map[string]time.Time{},
		localModTime: map[string]time.Time{},
		overlay:      newVisibilityOverlayState(),
	}
	view.entries["/"] = drive.Entry{ID: rootID, Name: "/", IsDir: true, ModTime: now, CreatedAt: now, UpdatedAt: now}
	return view
}

type vfsViewRuntime struct {
	v *VFS
}

func newVFSViewRuntime(v *VFS) vfsViewRuntime {
	return vfsViewRuntime{v: v}
}

func (r vfsViewRuntime) RebaseCachedPathsLocked(oldPath, newPath string) {
	oldPath = cleanVirtual(oldPath)
	newPath = cleanVirtual(newPath)
	for path, entry := range r.v.view.entries {
		if !isPathUnder(path, oldPath) {
			continue
		}
		nextPath := joinVirtual(newPath, strings.TrimPrefix(path, oldPath+"/"))
		delete(r.v.view.entries, path)
		r.v.view.entries[nextPath] = entry
	}
}

func (r vfsViewRuntime) MarkLocalDirLocked(path string) {
	r.v.view.localDirs[cleanVirtual(path)] = time.Now().Add(localCreateLookupTTL)
}

func (r vfsViewRuntime) IsRecentLocalDir(path string) bool {
	path = cleanVirtual(path)
	now := time.Now()
	r.v.view.mu.Lock()
	defer r.v.view.mu.Unlock()
	expires, ok := r.v.view.localDirs[path]
	if !ok {
		return false
	}
	if now.After(expires) {
		delete(r.v.view.localDirs, path)
		return false
	}
	return true
}

func (r vfsViewRuntime) RefreshPath(path string) {
	r.v.view.mu.Lock()
	delete(r.v.view.lists, cleanVirtual(path))
	r.v.view.mu.Unlock()
}

func (r vfsViewRuntime) InvalidateListLocked(path string) {
	delete(r.v.view.lists, cleanVirtual(path))
}

func (r vfsViewRuntime) ApplyLocalModTime(path string, entry drive.Entry) drive.Entry {
	r.v.view.mu.RLock()
	defer r.v.view.mu.RUnlock()
	return r.ApplyLocalModTimeLocked(path, entry)
}

func (r vfsViewRuntime) ApplyLocalModTimes(parentPath string, entries []drive.Entry) []drive.Entry {
	parentPath = cleanVirtual(parentPath)
	r.v.view.mu.RLock()
	defer r.v.view.mu.RUnlock()
	for i, entry := range entries {
		entries[i] = r.ApplyLocalModTimeLocked(joinVirtual(parentPath, entry.Name), entry)
	}
	return entries
}

func (r vfsViewRuntime) ApplyLocalModTimeLocked(path string, entry drive.Entry) drive.Entry {
	if modTime, ok := r.v.view.localModTime[cleanVirtual(path)]; ok && !modTime.IsZero() {
		entry.ModTime = modTime
		entry.UpdatedAt = modTime
	}
	return entry
}

func (r vfsViewRuntime) LocalModTimeFor(path string) time.Time {
	r.v.view.mu.RLock()
	defer r.v.view.mu.RUnlock()
	return r.v.view.localModTime[cleanVirtual(path)]
}

func (r vfsViewRuntime) SetLocalModTime(path string, modTime time.Time) {
	r.v.view.mu.Lock()
	r.SetLocalModTimeLocked(path, modTime)
	r.v.view.mu.Unlock()
}

func (r vfsViewRuntime) SetLocalModTimeLocked(path string, modTime time.Time) {
	if modTime.IsZero() {
		return
	}
	r.v.view.localModTime[cleanVirtual(path)] = modTime
}

func (r vfsViewRuntime) CommitEntryLocalModTime(path string, entry drive.Entry, modTime time.Time) {
	path = cleanVirtual(path)
	entry.ModTime = modTime
	r.v.view.mu.Lock()
	r.v.view.entries[path] = entry
	r.SetLocalModTimeLocked(path, modTime)
	r.InvalidateListLocked(filepath.Dir(path))
	r.v.view.mu.Unlock()
}

func (r vfsViewRuntime) ClearLocalModTime(path string) {
	r.v.view.mu.Lock()
	defer r.v.view.mu.Unlock()
	path = cleanVirtual(path)
	for knownPath := range r.v.view.localModTime {
		if knownPath == path || isPathUnder(knownPath, path) {
			delete(r.v.view.localModTime, knownPath)
		}
	}
}

func (r vfsViewRuntime) MoveLocalModTime(oldPath, newPath string) {
	r.v.view.mu.Lock()
	r.MoveLocalModTimeLocked(oldPath, newPath)
	r.v.view.mu.Unlock()
}

func (r vfsViewRuntime) MoveLocalModTimeLocked(oldPath, newPath string) {
	oldPath = cleanVirtual(oldPath)
	newPath = cleanVirtual(newPath)
	for knownPath, modTime := range r.v.view.localModTime {
		if knownPath == oldPath {
			delete(r.v.view.localModTime, knownPath)
			r.v.view.localModTime[newPath] = modTime
			continue
		}
		if isPathUnder(knownPath, oldPath) {
			nextPath := joinVirtual(newPath, strings.TrimPrefix(knownPath, oldPath+"/"))
			delete(r.v.view.localModTime, knownPath)
			r.v.view.localModTime[nextPath] = modTime
		}
	}
}

func (v *VFS) rebaseCachedPathsLocked(oldPath, newPath string) {
	newVFSViewRuntime(v).RebaseCachedPathsLocked(oldPath, newPath)
}
func (v *VFS) markLocalDirLocked(path string) {
	newVFSViewRuntime(v).MarkLocalDirLocked(path)
}
func (v *VFS) isRecentLocalDir(path string) bool {
	return newVFSViewRuntime(v).IsRecentLocalDir(path)
}

// RefreshPath clears the directory listing cache for path so the next List call
// fetches fresh data from the remote driver.
func (v *VFS) RefreshPath(path string) {
	newVFSViewRuntime(v).RefreshPath(path)
}
func (v *VFS) invalidateListLocked(path string) {
	newVFSViewRuntime(v).InvalidateListLocked(path)
}

func (v *VFS) applyLocalModTime(path string, entry drive.Entry) drive.Entry {
	return newVFSViewRuntime(v).ApplyLocalModTime(path, entry)
}
func (v *VFS) applyLocalModTimes(parentPath string, entries []drive.Entry) []drive.Entry {
	return newVFSViewRuntime(v).ApplyLocalModTimes(parentPath, entries)
}
func (v *VFS) applyLocalModTimeLocked(path string, entry drive.Entry) drive.Entry {
	return newVFSViewRuntime(v).ApplyLocalModTimeLocked(path, entry)
}
func (v *VFS) localModTimeFor(path string) time.Time {
	return newVFSViewRuntime(v).LocalModTimeFor(path)
}
func (v *VFS) setLocalModTime(path string, modTime time.Time) {
	newVFSViewRuntime(v).SetLocalModTime(path, modTime)
}
func (v *VFS) clearLocalModTime(path string) {
	newVFSViewRuntime(v).ClearLocalModTime(path)
}
func (v *VFS) moveLocalModTime(oldPath, newPath string) {
	newVFSViewRuntime(v).MoveLocalModTime(oldPath, newPath)
}
func (v *VFS) moveLocalModTimeLocked(oldPath, newPath string) {
	newVFSViewRuntime(v).MoveLocalModTimeLocked(oldPath, newPath)
}
