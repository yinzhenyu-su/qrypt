package vfs

import (
	"context"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const listCacheTTL = 10 * time.Second
const dirPrefetchLimit = 2
const dirPrefetchCooldown = 5 * time.Minute
const dirPrefetchTimeout = 15 * time.Second

type listCacheEntry struct {
	entries []drive.Entry
	expires time.Time
}

type listLoad struct {
	done     chan struct{}
	entries  []drive.Entry
	err      error
	prefetch bool
}

func (v *VFS) listChildren(ctx context.Context, parentPath, parentID string) ([]drive.Entry, error) {
	return v.listChildrenWithMode(ctx, parentPath, parentID, false)
}

func (v *VFS) prefetchChildren(ctx context.Context, parentPath, parentID string) ([]drive.Entry, error) {
	return v.listChildrenWithMode(ctx, parentPath, parentID, true)
}

func (v *VFS) listChildrenWithMode(ctx context.Context, parentPath, parentID string, prefetch bool) ([]drive.Entry, error) {
	parentPath = cleanVirtual(parentPath)
	for {
		now := time.Now()
		v.mu.RLock()
		cached, ok := v.lists[parentPath]
		if ok && now.Before(cached.expires) {
			entries := cloneEntries(cached.entries)
			v.mu.RUnlock()
			entries = v.applyLocalModTimes(parentPath, entries)
			return v.localChildren(parentPath, v.filterDeleted(parentPath, entries)), nil
		}
		v.mu.RUnlock()

		load, owner := v.beginListLoad(parentPath, prefetch)
		if !owner {
			select {
			case <-load.done:
				if load.err != nil {
					if load.prefetch && !prefetch && ctx.Err() == nil {
						continue
					}
					return nil, load.err
				}
				entries := cloneEntries(load.entries)
				entries = v.applyLocalModTimes(parentPath, entries)
				return v.localChildren(parentPath, v.filterDeleted(parentPath, entries)), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		entries, err := v.loadRemoteChildren(ctx, parentPath, parentID, prefetch)
		v.finishListLoad(parentPath, load, entries, err)
		return entries, err
	}
}

func (v *VFS) loadRemoteChildren(ctx context.Context, parentPath, parentID string, prefetch bool) ([]drive.Entry, error) {
	parentPath = cleanVirtual(parentPath)
	now := time.Now()
	entries, err := v.driver.List(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if prefetch {
		current, err := v.resolve(ctx, parentPath)
		if err != nil {
			return nil, err
		}
		if !current.IsDir || current.ID != parentID {
			return nil, fmt.Errorf("vfs: discard stale directory prefetch path=%q id=%q current_id=%q", parentPath, parentID, current.ID)
		}
	}
	v.updateOverlay(parentPath, entries)
	entries = v.filterDeleted(parentPath, entries)
	v.mu.Lock()
	for i, child := range entries {
		childPath := joinVirtual(parentPath, child.Name)
		child = v.applyLocalModTimeLocked(childPath, child)
		entries[i] = child
		v.entries[childPath] = child
	}
	v.lists[parentPath] = listCacheEntry{entries: cloneEntries(entries), expires: now.Add(listCacheTTL)}
	v.mu.Unlock()
	return v.localChildren(parentPath, entries), nil
}

func (v *VFS) beginListLoad(parentPath string, prefetch bool) (*listLoad, bool) {
	parentPath = cleanVirtual(parentPath)
	v.listLoadMu.Lock()
	defer v.listLoadMu.Unlock()
	if load := v.listLoads[parentPath]; load != nil {
		return load, false
	}
	load := &listLoad{done: make(chan struct{}), prefetch: prefetch}
	v.listLoads[parentPath] = load
	return load, true
}

func (v *VFS) finishListLoad(parentPath string, load *listLoad, entries []drive.Entry, err error) {
	parentPath = cleanVirtual(parentPath)
	if err == nil {
		load.entries = cloneEntries(entries)
	}
	load.err = err
	v.listLoadMu.Lock()
	if v.listLoads[parentPath] == load {
		delete(v.listLoads, parentPath)
	}
	v.listLoadMu.Unlock()
	close(load.done)
}

func (v *VFS) scheduleDirPrefetch(ctx context.Context, parentPath string, entries []drive.Entry) {
	parentPath = cleanVirtual(parentPath)
	dirs := make([]drive.Entry, 0)
	for _, entry := range entries {
		if entry.IsDir {
			dirs = append(dirs, entry)
		}
	}
	if len(dirs) == 0 {
		return
	}
	bgCtx := v.dirPrefetchCtx(ctx)
	go v.prefetchDirectDirs(bgCtx, parentPath, dirs)
}

func (v *VFS) prefetchDirectDirs(ctx context.Context, parentPath string, dirs []drive.Entry) {
	scheduled := 0
	for _, dir := range dirs {
		if ctx.Err() != nil {
			return
		}
		childPath := joinVirtual(parentPath, dir.Name)
		if !v.markDirPrefetch(childPath) {
			continue
		}
		scheduled++
		select {
		case v.dirPrefetchSem <- struct{}{}:
		case <-ctx.Done():
			v.finishDirPrefetch(childPath)
			return
		}
		if v.prefetchOneDir(ctx, childPath, dir.ID) {
			v.markDirPrefetchComplete(childPath)
		}
		<-v.dirPrefetchSem
	}
	if scheduled > 0 {
		logging.L.DebugfEvery("vfs.dir_prefetch_scheduled", time.Second, "[PREFETCH] child dirs scheduled parent=%q count=%d", parentPath, scheduled)
	}
}

func (v *VFS) prefetchOneDir(ctx context.Context, path, parentID string) bool {
	defer v.finishDirPrefetch(path)
	start := time.Now()
	opCtx, cancel := context.WithTimeout(ctx, dirPrefetchTimeout)
	defer cancel()
	entries, err := v.prefetchChildren(opCtx, path, parentID)
	if err != nil {
		if ctx.Err() == nil {
			logging.L.DebugfEvery("vfs.dir_prefetch_failed", time.Second, "[PREFETCH] list failed path=%q dur=%s err=%v", path, time.Since(start), err)
		}
		return false
	}
	logging.L.DebugfEvery("vfs.dir_prefetch_complete", time.Second, "[PREFETCH] list complete path=%q entries=%d dur=%s", path, len(entries), time.Since(start))
	return true
}

func (v *VFS) markDirPrefetch(path string) bool {
	path = cleanVirtual(path)
	if v.hasFreshListCache(path) {
		return false
	}
	now := time.Now()
	v.dirPrefetchMu.Lock()
	defer v.dirPrefetchMu.Unlock()
	if _, ok := v.dirPrefetching[path]; ok {
		return false
	}
	if last, ok := v.dirPrefetched[path]; ok && now.Sub(last) < dirPrefetchCooldown {
		return false
	}
	v.dirPrefetching[path] = struct{}{}
	return true
}

func (v *VFS) markDirPrefetchComplete(path string) {
	path = cleanVirtual(path)
	v.dirPrefetchMu.Lock()
	v.dirPrefetched[path] = time.Now()
	v.dirPrefetchMu.Unlock()
}

func (v *VFS) finishDirPrefetch(path string) {
	path = cleanVirtual(path)
	v.dirPrefetchMu.Lock()
	delete(v.dirPrefetching, path)
	v.dirPrefetchMu.Unlock()
}

func (v *VFS) hasFreshListCache(path string) bool {
	path = cleanVirtual(path)
	now := time.Now()
	v.mu.RLock()
	cached, ok := v.lists[path]
	v.mu.RUnlock()
	return ok && now.Before(cached.expires)
}

func (v *VFS) dirPrefetchCtx(fallback context.Context) context.Context {
	v.dirPrefetchMu.Lock()
	ctx := v.dirPrefetchContext
	v.dirPrefetchMu.Unlock()
	if ctx != nil && ctx.Err() == nil {
		return ctx
	}
	return fallback
}
