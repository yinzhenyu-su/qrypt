package vfs

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
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

func (v *VFS) List(ctx context.Context, path string) ([]drive.Entry, error) {
	entries, err := v.listNoPrefetch(ctx, path)
	v.recordHealthResult(drive.HealthOpList, err)
	if err != nil {
		return nil, err
	}
	if dirPrefetchEnabled(ctx) {
		v.scheduleDirPrefetch(ctx, cleanVirtual(path), entries)
	}
	return entries, nil
}

// ListPageResult is a deterministic slice of a directory listing. Entries are
// sorted by name (then id) so a name cursor stays stable while the directory
// changes between requests.
type ListPageResult struct {
	Entries    []drive.Entry `json:"entries,omitempty"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// ListPage returns up to limit entries of path, skipping entries whose name
// is <= cursor. The returned NextCursor is the name of the last returned
// entry when more entries remain, otherwise empty. limit <= 0 returns the
// whole (sorted) listing without a cursor.
func (v *VFS) ListPage(ctx context.Context, path string, cursor string, limit int) (ListPageResult, error) {
	entries, err := v.List(ctx, path)
	if err != nil {
		return ListPageResult{}, err
	}
	return paginateEntries(entries, cursor, limit), nil
}

func paginateEntries(entries []drive.Entry, cursor string, limit int) ListPageResult {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].ID < entries[j].ID
	})
	start := 0
	if cursor != "" {
		if c, ok := decodeListPageCursor(cursor); ok {
			// Skip everything before (name, id) so entries that share a name
			// with the cursor are not dropped on the next page.
			start = sort.Search(len(entries), func(i int) bool {
				if entries[i].Name != c.Name {
					return entries[i].Name > c.Name
				}
				return entries[i].ID > c.ID
			})
		}
	}
	if limit > 0 && start+limit < len(entries) {
		last := entries[start+limit-1]
		return ListPageResult{
			Entries:    entries[start : start+limit],
			NextCursor: encodeListPageCursor(last.Name, last.ID),
		}
	}
	return ListPageResult{Entries: entries[start:]}
}

// listPageCursor is the opaque cursor value returned in NextCursor. Encoding
// both name and id keeps paging correct when a directory contains entries
// that share the same name.
type listPageCursor struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

func encodeListPageCursor(name, id string) string {
	raw, err := json.Marshal(listPageCursor{Name: name, ID: id})
	if err != nil {
		return ""
	}
	return string(raw)
}

func decodeListPageCursor(cursor string) (listPageCursor, bool) {
	var c listPageCursor
	if err := json.Unmarshal([]byte(cursor), &c); err != nil {
		return listPageCursor{}, false
	}
	return c, true
}

func (v *VFS) listNoPrefetch(ctx context.Context, path string) ([]drive.Entry, error) {
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	entries, err := v.listChildren(ctx, path, entry.ID)
	if err != nil {
		return nil, err
	}
	entries = v.withPendingChildren(path, entries)
	return entries, nil
}

func (v *VFS) RemoteList(ctx context.Context, path string) ([]drive.Entry, error) {
	path = cleanVirtual(path)
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	if !entry.IsDir {
		return nil, fmt.Errorf("vfs: %s is not a directory", path)
	}
	entries, err := newVFSDriverRuntime(v).ListBackend().ListChildren(ctx, entry.ID)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

func (v *VFS) withPendingChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	return newVFSListingRuntime(v).PendingChildren(parentPath, entries)
}

func (v *VFS) listChildren(ctx context.Context, parentPath, parentID string) ([]drive.Entry, error) {
	return v.listChildrenWithMode(ctx, parentPath, parentID, false)
}

func (v *VFS) prefetchChildren(ctx context.Context, parentPath, parentID string) ([]drive.Entry, error) {
	return v.listChildrenWithMode(ctx, parentPath, parentID, true)
}

func (v *VFS) listChildrenWithMode(ctx context.Context, parentPath, parentID string, prefetch bool) ([]drive.Entry, error) {
	runtime := newVFSListingRuntime(v)
	scheduler := newVFSListScheduler(v)
	parentPath = cleanVirtual(parentPath)
	for {
		if v.isUnavailable(parentPath) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, parentPath)
		}
		now := time.Now()
		if entries, ok := runtime.FreshCachedList(parentPath, now); ok {
			return entries, nil
		}

		load, owner := scheduler.BeginListLoad(parentPath, prefetch)
		if !owner {
			select {
			case <-load.done:
				if load.err != nil {
					if load.prefetch && !prefetch && ctx.Err() == nil {
						if v.isUnavailable(parentPath) {
							return nil, fmt.Errorf("%w: %s", ErrNotFound, parentPath)
						}
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

		entries, err := loadRemoteChildrenWithRuntime(ctx, parentPath, parentID, prefetch, runtime, newVFSDriverRuntime(v).ListBackend())
		scheduler.FinishListLoad(parentPath, load, entries, err)
		return entries, err
	}
}

func loadRemoteChildrenWithRuntime(ctx context.Context, parentPath, parentID string, prefetch bool, runtime listingRuntime, backend listBackend) ([]drive.Entry, error) {
	parentPath = cleanVirtual(parentPath)
	now := time.Now()
	entries, err := backend.ListChildren(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if prefetch {
		if !runtime.IsCurrentPrefetchDir(parentPath, parentID) {
			return nil, fmt.Errorf("vfs: discard stale directory prefetch path=%q id=%q", parentPath, parentID)
		}
	}
	return runtime.CommitRemoteList(parentPath, entries, now.Add(listCacheTTL)), nil
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
	bgCtx := newVFSListScheduler(v).DirPrefetchContext(ctx)
	go v.prefetchDirectDirs(bgCtx, parentPath, dirs)
}

func (v *VFS) prefetchDirectDirs(ctx context.Context, parentPath string, dirs []drive.Entry) {
	scheduler := newVFSListScheduler(v)
	scheduled := 0
	for _, dir := range dirs {
		if ctx.Err() != nil {
			return
		}
		childPath := joinVirtual(parentPath, dir.Name)
		if !v.isCurrentPrefetchDir(childPath, dir.ID) {
			continue
		}
		if !scheduler.MarkDirPrefetch(childPath) {
			continue
		}
		scheduled++
		if !scheduler.AcquireDirPrefetchSlot(ctx) {
			scheduler.FinishDirPrefetch(childPath)
			return
		}
		if !v.isCurrentPrefetchDir(childPath, dir.ID) {
			scheduler.FinishDirPrefetch(childPath)
			scheduler.ReleaseDirPrefetchSlot()
			continue
		}
		if v.prefetchOneDir(ctx, childPath, dir.ID) {
			scheduler.MarkDirPrefetchComplete(childPath)
		}
		scheduler.ReleaseDirPrefetchSlot()
	}
	if scheduled > 0 {
		logging.L.DebugfEvery("vfs.dir_prefetch_scheduled", time.Second, "[PREFETCH] child dirs scheduled parent=%q count=%d", parentPath, scheduled)
	}
}

func (v *VFS) prefetchOneDir(ctx context.Context, path, parentID string) bool {
	defer newVFSListScheduler(v).FinishDirPrefetch(path)
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

func (v *VFS) suppressDirPrefetch(path string) {
	newVFSListScheduler(v).SuppressDirPrefetch(path)
}

func (v *VFS) isCurrentPrefetchDir(path, id string) bool {
	return newVFSListingRuntime(v).IsCurrentPrefetchDir(path, id)
}

type listingRuntime interface {
	FreshCachedList(parentPath string, now time.Time) ([]drive.Entry, bool)
	CommitRemoteList(parentPath string, entries []drive.Entry, expires time.Time) []drive.Entry
	PendingChildren(parentPath string, entries []drive.Entry) []drive.Entry
	IsCurrentPrefetchDir(path, id string) bool
}

type vfsListingRuntime struct {
	v *VFS
}

func newVFSListingRuntime(v *VFS) vfsListingRuntime {
	return vfsListingRuntime{v: v}
}

func (r vfsListingRuntime) FreshCachedList(parentPath string, now time.Time) ([]drive.Entry, bool) {
	parentPath = cleanVirtual(parentPath)
	r.v.view.mu.RLock()
	cached, ok := r.v.view.lists[parentPath]
	if ok && now.Before(cached.expires) {
		entries := cloneEntries(cached.entries)
		r.v.view.mu.RUnlock()
		entries = r.v.applyLocalModTimes(parentPath, entries)
		return r.v.localChildren(parentPath, r.v.filterDeleted(parentPath, entries)), true
	}
	r.v.view.mu.RUnlock()
	return nil, false
}

func (r vfsListingRuntime) CommitRemoteList(parentPath string, entries []drive.Entry, expires time.Time) []drive.Entry {
	parentPath = cleanVirtual(parentPath)
	r.v.updateOverlay(parentPath, entries)
	entries = r.v.filterDeleted(parentPath, entries)
	r.v.view.mu.Lock()
	for i, child := range entries {
		childPath := joinVirtual(parentPath, child.Name)
		child = r.v.applyLocalModTimeLocked(childPath, child)
		entries[i] = child
		r.v.view.entries.Set(childPath, child)
	}
	r.v.view.lists[parentPath] = listCacheEntry{entries: cloneEntries(entries), expires: expires}
	r.v.view.mu.Unlock()
	return r.v.localChildren(parentPath, entries)
}

func (r vfsListingRuntime) PendingChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	parentPath = cleanVirtual(parentPath)
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.Name] = true
	}
	for _, pending := range r.v.uploads.store.PendingUploads() {
		if filepath.Dir(pending.Path) != parentPath || seen[pending.Name] || r.v.isDeleted(pending.Path) {
			continue
		}
		entries = append(entries, drive.Entry{
			ID:        pending.FID,
			ParentID:  pending.ParentID,
			Name:      pending.Name,
			Size:      pending.Size,
			ModTime:   uploadModTime(pending),
			UpdatedAt: uploadModTime(pending),
		})
		seen[pending.Name] = true
	}
	return entries
}

func (r vfsListingRuntime) IsCurrentPrefetchDir(path, id string) bool {
	path = cleanVirtual(path)
	if r.v.isUnavailable(path) {
		return false
	}
	entry, ok := r.v.view.entries.Get(path)
	return ok && entry.IsDir && entry.ID == id
}

type listBackend interface {
	ListChildren(ctx context.Context, parentID string) ([]drive.Entry, error)
}

type driverListBackend struct {
	driver drive.Driver
}

func newDriverListBackend(driver drive.Driver) driverListBackend {
	return driverListBackend{driver: driver}
}

func (b driverListBackend) ListChildren(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return b.driver.List(ctx, parentID)
}

type vfsListScheduler struct {
	v *VFS
}

func newVFSListScheduler(v *VFS) vfsListScheduler {
	return vfsListScheduler{v: v}
}

func (s vfsListScheduler) BeginListLoad(parentPath string, prefetch bool) (*listLoad, bool) {
	parentPath = cleanVirtual(parentPath)
	s.v.listing.list.loadMu.Lock()
	defer s.v.listing.list.loadMu.Unlock()
	if load := s.v.listing.list.loads[parentPath]; load != nil {
		return load, false
	}
	load := &listLoad{done: make(chan struct{}), prefetch: prefetch}
	s.v.listing.list.loads[parentPath] = load
	return load, true
}

func (s vfsListScheduler) FinishListLoad(parentPath string, load *listLoad, entries []drive.Entry, err error) {
	parentPath = cleanVirtual(parentPath)
	if err == nil {
		load.entries = cloneEntries(entries)
	}
	load.err = err
	s.v.listing.list.loadMu.Lock()
	if s.v.listing.list.loads[parentPath] == load {
		delete(s.v.listing.list.loads, parentPath)
	}
	s.v.listing.list.loadMu.Unlock()
	close(load.done)
}

func (s vfsListScheduler) HasFreshListCache(path string) bool {
	path = cleanVirtual(path)
	now := time.Now()
	s.v.view.mu.RLock()
	cached, ok := s.v.view.lists[path]
	s.v.view.mu.RUnlock()
	return ok && now.Before(cached.expires)
}

func (s vfsListScheduler) MarkDirPrefetch(path string) bool {
	path = cleanVirtual(path)
	if s.HasFreshListCache(path) {
		return false
	}
	now := time.Now()
	s.v.listing.dirPrefetch.mu.Lock()
	defer s.v.listing.dirPrefetch.mu.Unlock()
	if _, ok := s.v.listing.dirPrefetch.inFlight[path]; ok {
		return false
	}
	if last, ok := s.v.listing.dirPrefetch.done[path]; ok && now.Sub(last) < dirPrefetchCooldown {
		return false
	}
	s.v.listing.dirPrefetch.inFlight[path] = struct{}{}
	return true
}

func (s vfsListScheduler) MarkDirPrefetchComplete(path string) {
	path = cleanVirtual(path)
	s.v.listing.dirPrefetch.mu.Lock()
	s.v.listing.dirPrefetch.done[path] = time.Now()
	s.v.listing.dirPrefetch.mu.Unlock()
}

func (s vfsListScheduler) SuppressDirPrefetch(path string) {
	path = cleanVirtual(path)
	s.v.listing.dirPrefetch.mu.Lock()
	s.v.listing.dirPrefetch.done[path] = time.Now()
	s.v.listing.dirPrefetch.mu.Unlock()
}

func (s vfsListScheduler) FinishDirPrefetch(path string) {
	path = cleanVirtual(path)
	s.v.listing.dirPrefetch.mu.Lock()
	delete(s.v.listing.dirPrefetch.inFlight, path)
	s.v.listing.dirPrefetch.mu.Unlock()
}

func (s vfsListScheduler) AcquireDirPrefetchSlot(ctx context.Context) bool {
	select {
	case s.v.listing.dirPrefetch.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s vfsListScheduler) ReleaseDirPrefetchSlot() {
	<-s.v.listing.dirPrefetch.sem
}

func (s vfsListScheduler) StartDirPrefetch(ctx context.Context) bool {
	s.v.listing.dirPrefetch.mu.Lock()
	defer s.v.listing.dirPrefetch.mu.Unlock()
	if s.v.listing.dirPrefetch.started {
		return false
	}
	s.v.listing.dirPrefetch.started = true
	s.v.listing.dirPrefetch.context = ctx
	return true
}

func (s vfsListScheduler) DirPrefetchContext(fallback context.Context) context.Context {
	s.v.listing.dirPrefetch.mu.Lock()
	ctx := s.v.listing.dirPrefetch.context
	s.v.listing.dirPrefetch.mu.Unlock()
	if ctx != nil && ctx.Err() == nil {
		return ctx
	}
	return fallback
}

// listingState groups the directory-listing domain state: list
// coalescing and directory prefetch. It is separate from the read domain
// because it serves directory browsing (List) rather than file content
// (Read). listState and dirPrefetchState each guard their own mutex; the
// domain holds no persistent resources, so there is no Close - directory
// prefetch runs on the VFS lifecycle context and stops with it.
type listingState struct {
	list        *listState
	dirPrefetch *dirPrefetchState
}

// newListingState builds the listing domain state together.
func newListingState() *listingState {
	return &listingState{
		list:        newListState(),
		dirPrefetch: newDirPrefetchState(),
	}
}
