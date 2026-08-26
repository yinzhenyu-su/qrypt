package view

import (
	pathpkg "path"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Runtime is the view-domain operation surface: entry cache, local markers,
// local modtimes and directory-list caches. pkg/vfs adapters depend on it
// instead of touching the View state directly.
type Runtime struct {
	view *View
}

// NewRuntime builds a runtime over a view.
func NewRuntime(view *View) Runtime {
	return Runtime{view: view}
}

// CachedEntry returns the cached entry identity for a path.
func (r Runtime) CachedEntry(path string) (drive.Entry, bool) {
	return r.view.entries.Get(vfstypes.CleanVirtualPath(path))
}

// RebaseCachedPathsLocked rewrites every cached path under oldPath onto
// newPath (rename commit). Callers hold view.mu.
func (r Runtime) RebaseCachedPathsLocked(oldPath, newPath string) {
	oldPath = vfstypes.CleanVirtualPath(oldPath)
	newPath = vfstypes.CleanVirtualPath(newPath)
	r.view.entries.Range(func(path string, entry drive.Entry) bool {
		if !vfstypes.IsPathUnder(path, oldPath) {
			return true
		}
		nextPath := vfstypes.JoinVirtualPath(newPath, strings.TrimPrefix(path, oldPath+"/"))
		r.view.entries.Delete(path)
		r.view.entries.Set(nextPath, entry)
		return true
	})
}

// MarkLocalDirLocked marks a directory as locally created so resolves short-
// circuit to the local view for a while. Callers hold view.mu.
func (r Runtime) MarkLocalDirLocked(path string) {
	r.view.localDirs[vfstypes.CleanVirtualPath(path)] = time.Now().Add(LocalCreateLookupTTL)
}

// IsRecentLocalDir reports whether path is a recently created local dir that
// should resolve locally (expired markers are dropped).
func (r Runtime) IsRecentLocalDir(path string) bool {
	path = vfstypes.CleanVirtualPath(path)
	now := time.Now()
	r.view.mu.Lock()
	defer r.view.mu.Unlock()
	expires, ok := r.view.localDirs[path]
	if !ok {
		return false
	}
	if now.After(expires) {
		delete(r.view.localDirs, path)
		return false
	}
	return true
}

// RefreshPath drops the cached listing for path (next listing refetches).
func (r Runtime) RefreshPath(path string) {
	r.view.mu.Lock()
	delete(r.view.lists, vfstypes.CleanVirtualPath(path))
	r.view.mu.Unlock()
}

// InvalidateListLocked drops the cached listing for path. Callers hold
// view.mu.
func (r Runtime) InvalidateListLocked(path string) {
	delete(r.view.lists, vfstypes.CleanVirtualPath(path))
}

// ApplyLocalModTime overrides entry modtimes from the local-modtime map.
func (r Runtime) ApplyLocalModTime(path string, entry drive.Entry) drive.Entry {
	r.view.mu.RLock()
	defer r.view.mu.RUnlock()
	return r.ApplyLocalModTimeLocked(path, entry)
}

// ApplyLocalModTimes applies local modtimes to every child under parentPath.
func (r Runtime) ApplyLocalModTimes(parentPath string, entries []drive.Entry) []drive.Entry {
	parentPath = vfstypes.CleanVirtualPath(parentPath)
	r.view.mu.RLock()
	defer r.view.mu.RUnlock()
	for i, entry := range entries {
		entries[i] = r.ApplyLocalModTimeLocked(vfstypes.JoinVirtualPath(parentPath, entry.Name), entry)
	}
	return entries
}

// ApplyLocalModTimeLocked overrides entry modtimes from the local-modtime
// map. Callers hold view.mu (RLock is fine).
func (r Runtime) ApplyLocalModTimeLocked(path string, entry drive.Entry) drive.Entry {
	if modTime, ok := r.view.localModTime[vfstypes.CleanVirtualPath(path)]; ok && !modTime.IsZero() {
		entry.ModTime = modTime
		entry.UpdatedAt = modTime
	}
	return entry
}

// LocalModTimeFor returns the recorded local modtime for path.
func (r Runtime) LocalModTimeFor(path string) time.Time {
	r.view.mu.RLock()
	defer r.view.mu.RUnlock()
	return r.view.localModTime[vfstypes.CleanVirtualPath(path)]
}

// SetLocalModTime records a local modtime override for path.
func (r Runtime) SetLocalModTime(path string, modTime time.Time) {
	r.view.mu.Lock()
	r.SetLocalModTimeLocked(path, modTime)
	r.view.mu.Unlock()
}

// SetLocalModTimeLocked records a local modtime override. Callers hold
// view.mu.
func (r Runtime) SetLocalModTimeLocked(path string, modTime time.Time) {
	if modTime.IsZero() {
		return
	}
	r.view.localModTime[vfstypes.CleanVirtualPath(path)] = modTime
}

// CommitEntryLocalModTime writes a committed entry with a local modtime
// override and invalidates its parent list cache in one critical section.
func (r Runtime) CommitEntryLocalModTime(path string, entry drive.Entry, modTime time.Time) {
	path = vfstypes.CleanVirtualPath(path)
	entry.ModTime = modTime
	r.view.mu.Lock()
	r.view.entries.Set(path, entry)
	r.SetLocalModTimeLocked(path, modTime)
	r.InvalidateListLocked(pathpkg.Dir(path))
	r.view.mu.Unlock()
}

// ClearLocalModTime drops the local modtime override for path and every
// descendant.
func (r Runtime) ClearLocalModTime(path string) {
	r.view.mu.Lock()
	defer r.view.mu.Unlock()
	path = vfstypes.CleanVirtualPath(path)
	for knownPath := range r.view.localModTime {
		if knownPath == path || vfstypes.IsPathUnder(knownPath, path) {
			delete(r.view.localModTime, knownPath)
		}
	}
}

// MoveLocalModTime relocates local modtime overrides from oldPath to newPath
// (rename commit).
func (r Runtime) MoveLocalModTime(oldPath, newPath string) {
	r.view.mu.Lock()
	r.MoveLocalModTimeLocked(oldPath, newPath)
	r.view.mu.Unlock()
}

// MoveLocalModTimeLocked relocates local modtime overrides. Callers hold
// view.mu.
func (r Runtime) MoveLocalModTimeLocked(oldPath, newPath string) {
	oldPath = vfstypes.CleanVirtualPath(oldPath)
	newPath = vfstypes.CleanVirtualPath(newPath)
	for knownPath, modTime := range r.view.localModTime {
		if knownPath == oldPath {
			delete(r.view.localModTime, knownPath)
			r.view.localModTime[newPath] = modTime
			continue
		}
		if vfstypes.IsPathUnder(knownPath, oldPath) {
			nextPath := vfstypes.JoinVirtualPath(newPath, strings.TrimPrefix(knownPath, oldPath+"/"))
			delete(r.view.localModTime, knownPath)
			r.view.localModTime[nextPath] = modTime
		}
	}
}

// CacheListedChildren warms the entry cache with a freshly fetched remote
// listing (query recovery / cache warming, not a mutation commit).
func (r Runtime) CacheListedChildren(parentPath string, entries []drive.Entry) {
	r.view.mu.Lock()
	defer r.view.mu.Unlock()
	for _, child := range entries {
		r.view.entries.Set(vfstypes.JoinVirtualPath(parentPath, child.Name), child)
	}
}

// FreshList returns the cached listing for parentPath when it is still
// fresh, or (nil, false) to force a refetch.
func (r Runtime) FreshList(parentPath string, now time.Time) ([]drive.Entry, bool) {
	parentPath = vfstypes.CleanVirtualPath(parentPath)
	r.view.mu.RLock()
	cached, ok := r.view.lists[parentPath]
	r.view.mu.RUnlock()
	if !ok || !now.Before(cached.expires) {
		return nil, false
	}
	return CloneEntries(cached.entries), true
}

// CommitChildren folds a fresh remote listing into the view under the view
// lock: local modtimes are applied, entries cached, and the list cache
// refreshed. The returned slice is the caller's to project further.
func (r Runtime) CommitChildren(parentPath string, entries []drive.Entry, expires time.Time) []drive.Entry {
	parentPath = vfstypes.CleanVirtualPath(parentPath)
	r.view.mu.Lock()
	for i, child := range entries {
		childPath := vfstypes.JoinVirtualPath(parentPath, child.Name)
		entries[i] = r.ApplyLocalModTimeLocked(childPath, child)
		r.view.entries.Set(childPath, child)
	}
	r.view.lists[parentPath] = listCacheEntry{entries: CloneEntries(entries), expires: expires}
	r.view.mu.Unlock()
	return entries
}

// PrepareLocalDirectoryCopy hides every cached child of path (plus the
// caller's hide set), drops path's list cache, marks it a recent local dir
// and records copy-hidden children so the half-populated remote state is not
// surfaced while the copy fills in.
func (r Runtime) PrepareLocalDirectoryCopy(path string, hideNames map[string]time.Time) {
	r.view.entries.Range(func(cachedPath string, cachedEntry drive.Entry) bool {
		if pathpkg.Dir(cachedPath) == path {
			if _, ok := hideNames[cachedEntry.Name]; !ok && !IsAppleMetadataName(cachedEntry.Name) {
				hideNames[cachedEntry.Name] = time.Now().Add(DirectoryCopyHideTTL)
			}
			r.view.entries.Delete(cachedPath)
		}
		return true
	})
	path = vfstypes.CleanVirtualPath(path)
	r.view.mu.Lock()
	r.view.localDirs[path] = time.Now().Add(LocalCreateLookupTTL)
	delete(r.view.lists, path)
	r.view.mu.Unlock()
	overlay := r.view.Overlay()
	overlay.mu.Lock()
	if len(hideNames) == 0 {
		delete(overlay.copyHiddenChildren, path)
	} else {
		overlay.copyHiddenChildren[path] = hideNames
	}
	overlay.mu.Unlock()
}
