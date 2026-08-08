package listing

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Lister implements directory listing on top of a Host and State.
type Lister struct {
	host  Host
	state *State
}

func NewLister(host Host, state *State) *Lister {
	return &Lister{host: host, state: state}
}

// State returns the listing domain state.
func (l *Lister) State() *State { return l.state }

// List returns the (pending-inclusive) children of path.
func (l *Lister) List(ctx context.Context, path string) ([]drive.Entry, error) {
	entries, err := l.ListNoPrefetch(ctx, path)
	l.host.RecordHealth(drive.HealthOpList, err)
	if err != nil {
		return nil, err
	}
	if DirPrefetchEnabled(ctx) {
		l.ScheduleDirPrefetch(ctx, CleanVirtualPath(path), entries)
	}
	return entries, nil
}

// ListPageResult is a deterministic slice of a directory listing. Entries
// are sorted by name (then id) so a name cursor stays stable while the
// directory changes between requests.
type ListPageResult struct {
	Entries    []drive.Entry `json:"entries,omitempty"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// ListPage returns up to limit entries of path, skipping entries whose
// name is <= cursor. The returned NextCursor is the name of the last
// returned entry when more entries remain, otherwise empty. limit <= 0
// returns the whole (sorted) listing without a cursor.
func (l *Lister) ListPage(ctx context.Context, path string, cursor string, limit int) (ListPageResult, error) {
	entries, err := l.List(ctx, path)
	if err != nil {
		return ListPageResult{}, err
	}
	return PaginateEntries(entries, cursor, limit), nil
}

// PaginateEntries returns a deterministic slice of a listing.
func PaginateEntries(entries []drive.Entry, cursor string, limit int) ListPageResult {
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

// listPageCursor is the opaque cursor value returned in NextCursor.
// Encoding both name and id keeps paging correct when a directory contains
// entries that share the same name.
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

// ListNoPrefetch lists a directory without scheduling prefetch.
func (l *Lister) ListNoPrefetch(ctx context.Context, path string) ([]drive.Entry, error) {
	entry, err := l.host.Resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	entries, err := l.Children(ctx, path, entry.ID)
	if err != nil {
		return nil, err
	}
	entries = l.pendingChildren(path, entries)
	return entries, nil
}

// RemoteList lists a directory directly from the driver, bypassing the
// local view.
func (l *Lister) RemoteList(ctx context.Context, path string) ([]drive.Entry, error) {
	path = CleanVirtualPath(path)
	entry, err := l.host.Resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	if !entry.IsDir {
		return nil, fmt.Errorf("vfs: %s is not a directory", path)
	}
	entries, err := l.host.ListChildren(ctx, entry.ID)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

func (l *Lister) pendingChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.Name] = true
	}
	for _, pending := range l.host.PendingUploads() {
		if filepath.Dir(pending.Path) != parentPath || seen[pending.Name] || l.host.IsDeleted(pending.Path) {
			continue
		}
		entries = append(entries, drive.Entry{
			ID:        pending.FID,
			ParentID:  pending.ParentID,
			Name:      pending.Name,
			Size:      pending.Size,
			ModTime:   ModTime(pending),
			UpdatedAt: ModTime(pending),
		})
		seen[pending.Name] = true
	}
	return entries
}

// Children lists the (pending-inclusive) children of a directory,
// serving from cache when fresh and coalescing concurrent loads.
func (l *Lister) Children(ctx context.Context, parentPath, parentID string) ([]drive.Entry, error) {
	return l.listChildrenWithMode(ctx, parentPath, parentID, false)
}

func (l *Lister) prefetchChildren(ctx context.Context, parentPath, parentID string) ([]drive.Entry, error) {
	return l.listChildrenWithMode(ctx, parentPath, parentID, true)
}

func (l *Lister) listChildrenWithMode(ctx context.Context, parentPath, parentID string, prefetch bool) ([]drive.Entry, error) {
	parentPath = CleanVirtualPath(parentPath)
	for {
		if l.host.IsUnavailable(parentPath) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, parentPath)
		}
		now := time.Now()
		if entries, ok := l.host.FreshListCache(parentPath, now); ok {
			return entries, nil
		}

		load, owner := l.state.BeginListLoad(parentPath, prefetch)
		if !owner {
			select {
			case <-load.done:
				if load.err != nil {
					if load.prefetch && !prefetch && ctx.Err() == nil {
						if l.host.IsUnavailable(parentPath) {
							return nil, fmt.Errorf("%w: %s", ErrNotFound, parentPath)
						}
						continue
					}
					return nil, load.err
				}
				entries := cloneEntries(load.entries)
				entries = l.host.ApplyLocalModTimes(parentPath, entries)
				return l.host.LocalChildren(parentPath, l.host.FilterDeleted(parentPath, entries)), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		entries, err := loadRemoteChildrenWithRuntime(ctx, parentPath, parentID, prefetch, l)
		l.state.FinishListLoad(parentPath, load, entries, err)
		return entries, err
	}
}

func loadRemoteChildrenWithRuntime(ctx context.Context, parentPath, parentID string, prefetch bool, l *Lister) ([]drive.Entry, error) {
	parentPath = CleanVirtualPath(parentPath)
	now := time.Now()
	entries, err := l.host.ListChildren(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if prefetch {
		if !l.IsCurrentPrefetchDir(parentPath, parentID) {
			return nil, fmt.Errorf("vfs: discard stale directory prefetch path=%q id=%q", parentPath, parentID)
		}
	}
	return l.commitRemoteList(parentPath, entries, now.Add(listCacheTTL)), nil
}

func (l *Lister) commitRemoteList(parentPath string, entries []drive.Entry, expires time.Time) []drive.Entry {
	parentPath = CleanVirtualPath(parentPath)
	l.host.UpdateOverlay(parentPath, entries)
	entries = l.host.FilterDeleted(parentPath, entries)
	for i, child := range entries {
		childPath := joinVirtual(parentPath, child.Name)
		entries[i] = l.host.ApplyLocalModTimeLocked(childPath, child)
	}
	return l.host.LocalChildren(parentPath, l.host.CommitList(parentPath, entries, expires))
}

// IsCurrentPrefetchDir reports whether path still resolves to id (the
// directory a prefetch was started for).
func (l *Lister) IsCurrentPrefetchDir(path, id string) bool {
	path = CleanVirtualPath(path)
	if l.host.IsUnavailable(path) {
		return false
	}
	entry, ok := l.host.GetEntry(path)
	return ok && entry.IsDir && entry.ID == id
}

// ScheduleDirPrefetch schedules background prefetch of child directories.
func (l *Lister) ScheduleDirPrefetch(ctx context.Context, parentPath string, entries []drive.Entry) {
	parentPath = CleanVirtualPath(parentPath)
	dirs := make([]drive.Entry, 0)
	for _, entry := range entries {
		if entry.IsDir {
			dirs = append(dirs, entry)
		}
	}
	if len(dirs) == 0 {
		return
	}
	bgCtx := l.state.DirPrefetchContext(ctx)
	go l.prefetchDirectDirs(bgCtx, parentPath, dirs)
}

func (l *Lister) prefetchDirectDirs(ctx context.Context, parentPath string, dirs []drive.Entry) {
	scheduled := 0
	for _, dir := range dirs {
		if ctx.Err() != nil {
			return
		}
		childPath := joinVirtual(parentPath, dir.Name)
		if !l.IsCurrentPrefetchDir(childPath, dir.ID) {
			continue
		}
		if !l.state.MarkDirPrefetch(childPath, l.HasFreshListCache(childPath)) {
			continue
		}
		scheduled++
		if !l.state.AcquireDirPrefetchSlot(ctx) {
			l.state.FinishDirPrefetch(childPath)
			return
		}
		if !l.IsCurrentPrefetchDir(childPath, dir.ID) {
			l.state.FinishDirPrefetch(childPath)
			l.state.ReleaseDirPrefetchSlot()
			continue
		}
		if l.prefetchOneDir(ctx, childPath, dir.ID) {
			l.state.MarkDirPrefetchComplete(childPath)
		}
		l.state.ReleaseDirPrefetchSlot()
	}
	if scheduled > 0 {
		logging.L.DebugfEvery("vfs.dir_prefetch_scheduled", time.Second, "[PREFETCH] child dirs scheduled parent=%q count=%d", parentPath, scheduled)
	}
}

func (l *Lister) prefetchOneDir(ctx context.Context, path, parentID string) bool {
	defer l.state.FinishDirPrefetch(path)
	start := time.Now()
	opCtx, cancel := context.WithTimeout(ctx, dirPrefetchTimeout)
	defer cancel()
	entries, err := l.prefetchChildren(opCtx, path, parentID)
	if err != nil {
		if ctx.Err() == nil {
			logging.L.DebugfEvery("vfs.dir_prefetch_failed", time.Second, "[PREFETCH] list failed path=%q dur=%s err=%v", path, time.Since(start), err)
		}
		return false
	}
	logging.L.DebugfEvery("vfs.dir_prefetch_complete", time.Second, "[PREFETCH] list complete path=%q entries=%d dur=%s", path, len(entries), time.Since(start))
	return true
}

// SuppressDirPrefetch marks a directory as recently prefetched.
func (l *Lister) SuppressDirPrefetch(path string) {
	l.state.SuppressDirPrefetch(path)
}

// HasFreshListCache reports whether path has a fresh cached listing.
func (l *Lister) HasFreshListCache(path string) bool {
	path = CleanVirtualPath(path)
	now := time.Now()
	_, ok := l.host.FreshListCache(path, now)
	return ok
}

// StartDirPrefetch records the lifecycle context for background prefetch.
func (l *Lister) StartDirPrefetch(ctx context.Context) bool {
	return l.state.StartDirPrefetch(ctx)
}

func joinVirtual(parent, name string) string {
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

// CleanVirtualPath normalizes qrypt virtual paths to absolute slash paths.
func CleanVirtualPath(path string) string {
	return vfstypes.CleanVirtualPath(path)
}

// ErrNotFound is reported for paths hidden by the overlay.
var ErrNotFound = drive.ErrNotFound
