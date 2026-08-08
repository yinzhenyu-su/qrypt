package vfs

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/internal/vfs/listing"
	"github.com/yinzhenyu/qrypt/internal/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// listingState aliases the listing domain state (internal/listing).
type listCacheEntry struct {
	entries []drive.Entry
	expires time.Time
}

// listBackend abstracts the driver's child listing for tests.
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

func (v *VFS) listNoPrefetch(ctx context.Context, path string) ([]drive.Entry, error) {
	return v.lister.ListNoPrefetch(ctx, path)
}

func (v *VFS) listChildren(ctx context.Context, parentPath, parentID string) ([]drive.Entry, error) {
	return v.lister.Children(ctx, parentPath, parentID)
}

var paginateEntries = listing.PaginateEntries

type listingState = listing.State

// vfsListingRemote adapts VFS internals to listing.Remote: resolution and
// backend child listing only.
type vfsListingRemote struct {
	v *VFS
}

func newVFSListingRemote(v *VFS) vfsListingRemote {
	return vfsListingRemote{v: v}
}

func (h vfsListingRemote) Resolve(ctx context.Context, path string) (drive.Entry, error) {
	return h.v.resolve(ctx, path)
}

func (h vfsListingRemote) ListChildren(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return newVFSDriverRuntime(h.v).ListBackend().ListChildren(ctx, parentID)
}

// vfsListingView adapts VFS internals to listing.View: the synthesized
// directory view (overlay + fresh-list cache + pending projection).
type vfsListingView struct {
	v *VFS
}

func newVFSListingView(v *VFS) vfsListingView {
	return vfsListingView{v: v}
}

func (h vfsListingView) IsUnavailable(path string) bool {
	return h.v.isUnavailable(path)
}

func (h vfsListingView) IsDeleted(path string) bool {
	return h.v.isDeleted(path)
}

func (h vfsListingView) GetEntry(path string) (drive.Entry, bool) {
	return h.v.view.entries.Get(path)
}

func (h vfsListingView) FreshListCache(parentPath string, now time.Time) ([]drive.Entry, bool) {
	parentPath = cleanVirtual(parentPath)
	h.v.view.mu.RLock()
	cached, ok := h.v.view.lists[parentPath]
	h.v.view.mu.RUnlock()
	if !ok || !now.Before(cached.expires) {
		return nil, false
	}
	return h.projectChildren(parentPath, cached.entries), true
}

// CommitRemoteChildren folds a fresh remote listing into the synthesized
// view atomically: rename/delete overlay update, filtering of invisible
// remote nodes, local-modtime application under the view lock, entry and
// list-cache commit, and local-children merge. The listing domain never
// sees the overlay/cache orchestration or the locks.
func (h vfsListingView) CommitRemoteChildren(parentPath string, remote []drive.Entry, expires time.Time) []drive.Entry {
	parentPath = cleanVirtual(parentPath)
	h.v.updateOverlay(parentPath, remote)
	entries := h.v.filterDeleted(parentPath, remote)
	h.v.view.mu.Lock()
	for i, child := range entries {
		childPath := joinVirtual(parentPath, child.Name)
		entries[i] = h.v.applyLocalModTimeLocked(childPath, child)
		h.v.view.entries.Set(childPath, child)
	}
	h.v.view.lists[parentPath] = listCacheEntry{entries: cloneEntries(entries), expires: expires}
	h.v.view.mu.Unlock()
	return h.projectChildren(parentPath, entries)
}

// projectChildren applies the current visibility state to an entry
// snapshot: local modtimes, deleted/hidden filtering, and local-children
// merge. Shared by FreshListCache, CommitRemoteChildren, and the in-flight
// waiter projection (via listing.View.ProjectChildren).
func (h vfsListingView) projectChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	// ApplyLocalModTimes writes modtimes in place; clone first so a shared
	// snapshot (e.g. concurrent waiters projecting the same owner load) is
	// never mutated and the caller's slice stays untouched.
	entries = cloneEntries(entries)
	entries = h.v.applyLocalModTimes(parentPath, entries)
	entries = h.v.filterDeleted(parentPath, entries)
	return h.v.localChildren(parentPath, entries)
}

func (h vfsListingView) ProjectChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	return h.projectChildren(parentPath, entries)
}

func (h vfsListingView) PendingUploads() []vfstypes.PendingUpload {
	return h.v.uploads.Store().PendingUploads()
}

// List serves a directory listing (pending-inclusive).
func (v *VFS) List(ctx context.Context, path string) ([]drive.Entry, error) {
	return v.lister.List(ctx, path)
}

// ListPage returns a deterministic slice of a directory listing.
func (v *VFS) ListPage(ctx context.Context, path string, cursor string, limit int) (ListPageResult, error) {
	return v.lister.ListPage(ctx, path, cursor, limit)
}

// RemoteList lists a directory directly from the driver.
func (v *VFS) RemoteList(ctx context.Context, path string) ([]drive.Entry, error) {
	return v.lister.RemoteList(ctx, path)
}

// ListPageResult is the deterministic page type returned by ListPage.
type ListPageResult = listing.ListPageResult

func (v *VFS) suppressDirPrefetch(path string) {
	v.lister.SuppressDirPrefetch(path)
}

func (v *VFS) isCurrentPrefetchDir(path, id string) bool {
	return v.lister.IsCurrentPrefetchDir(path, id)
}

func (v *VFS) hasFreshListCache(path string) bool {
	return v.lister.HasFreshListCache(path)
}

func (v *VFS) startDirPrefetch(ctx context.Context) bool {
	return v.lister.StartDirPrefetch(ctx)
}

func (v *VFS) scheduleDirPrefetch(ctx context.Context, path string, entries []drive.Entry) {
	v.lister.ScheduleDirPrefetch(ctx, path, entries)
}

// vfsListingHealth adapts the drive health tracker to listing.HealthRecorder.
// It holds only the tracker - not the whole VFS - so the listing adapter
// carries exactly the dependency it uses.
type vfsListingHealth struct {
	tracker *drive.HealthTracker
}

func (h vfsListingHealth) RecordResult(op string, err error) {
	h.tracker.RecordResult(op, err)
}

var _ listing.HealthRecorder = vfsListingHealth{}
var _ listing.Remote = vfsListingRemote{}
var _ listing.View = vfsListingView{}
