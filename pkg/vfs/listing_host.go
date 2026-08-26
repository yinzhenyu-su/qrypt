package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/listing"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
	"path/filepath"
	"time"
)

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
	resolver pathResolver
	driver   drive.Driver
}

func newVFSListingRemote(v *VFS) vfsListingRemote {
	return vfsListingRemote{resolver: v, driver: v.driver}
}

func (h vfsListingRemote) Resolve(ctx context.Context, path string) (drive.Entry, error) {
	return h.resolver.resolve(ctx, path)
}

func (h vfsListingRemote) ListChildren(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return h.driver.List(ctx, parentID)
}

// vfsListingView adapts VFS internals to listing.View: the synthesized
// directory view (overlay visibility + fresh-list cache + pending
// projection). The view-domain steps (visibility filtering, local-children
// merge, list-cache commit) delegate to the view package; only the pending
// upload projection is VFS-specific.
type vfsListingView struct {
	vis   view.Visibility
	rt    view.Runtime
	store *uploadStore
}

func newVFSListingView(v *VFS) vfsListingView {
	return vfsListingView{vis: newVFSVisibilityRuntime(v), rt: view.NewRuntime(v.view), store: v.uploads.Store()}
}

func (h vfsListingView) IsUnavailable(path string) bool {
	return h.vis.IsUnavailable(path)
}

// Entry returns the cached entry identity. A miss does not mean the path
// is unavailable - the listing domain separates visibility from cache
// identity (callers of Children already hold the parentID from resolve).
func (h vfsListingView) Entry(path string) (drive.Entry, bool) {
	return h.rt.CachedEntry(path)
}

func (h vfsListingView) FreshListCache(parentPath string, now time.Time) ([]drive.Entry, bool) {
	entries, ok := h.rt.FreshList(parentPath, now)
	if !ok {
		return nil, false
	}
	return h.projectChildren(parentPath, entries), true
}

// CommitRemoteChildren folds a fresh remote listing into the synthesized
// view as a single semantic view operation: rename/delete overlay update,
// filtering of invisible remote nodes, local-modtime application under the
// view lock, entry and list-cache commit, and local-children merge. The
// steps span overlay/cache/local locks, so it is NOT an atomic snapshot -
// it encapsulates the ordered commit protocol. The listing domain never
// sees the overlay/cache orchestration or the locks.
func (h vfsListingView) CommitRemoteChildren(parentPath string, remote []drive.Entry, expires time.Time) []drive.Entry {
	h.vis.UpdateRenameOverlay(parentPath, remote)
	entries := h.vis.FilterDeleted(parentPath, remote)
	entries = h.rt.CommitChildren(parentPath, entries, expires)
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
	entries = view.CloneEntries(entries)
	entries = h.rt.ApplyLocalModTimes(parentPath, entries)
	entries = h.vis.FilterDeleted(parentPath, entries)
	entries = h.vis.LocalChildren(parentPath, entries)
	return h.mergePendingChildren(parentPath, entries)
}

// mergePendingChildren merges pending upload records into a projected
// listing: a pending file is visible under its parent unless a remote or
// local child with the same name already exists or the path is deleted.
// The pending projection is dynamic - it is recomputed on every read, so a
// pending record appearing or vanishing never requires cache invalidation.
func (h vfsListingView) mergePendingChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	parentPath = vfstypes.CleanVirtualPath(parentPath)
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.Name] = true
	}
	for _, pending := range h.store.PendingUploads() {
		if filepath.Dir(pending.Path) != parentPath || seen[pending.Name] || h.vis.IsDeleted(pending.Path) {
			continue
		}
		modTime := pendingModTime(pending)
		entries = append(entries, drive.Entry{
			ID:        pending.FID,
			ParentID:  pending.ParentID,
			Name:      pending.Name,
			Size:      pending.Size,
			ModTime:   modTime,
			UpdatedAt: modTime,
		})
		seen[pending.Name] = true
	}
	return entries
}

// pendingModTime returns the display mod time for a pending upload (zero
// when the record has no mtime set).
func pendingModTime(p PendingUpload) time.Time {
	if p.ModTime > 0 {
		return time.Unix(0, p.ModTime)
	}
	if p.UpdatedAt > 0 {
		return time.Unix(0, p.UpdatedAt)
	}
	return time.Time{}
}

func (h vfsListingView) ProjectChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	return h.projectChildren(parentPath, entries)
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
