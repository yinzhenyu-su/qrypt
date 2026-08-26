package view

import (
	"context"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// resolver is the local-resolution boundary ResolveWithRuntime needs: the
// view-side entry cache, the commit of a freshly fetched remote listing, and
// visibility checks.
type resolver interface {
	CachedEntry(path string) (drive.Entry, bool)
	CommitResolvedChildren(parentPath, name string, entries []drive.Entry) (drive.Entry, bool)
	IsUnavailable(path string) bool
	IsRecentLocalDir(path string) bool
}

// Resolve resolves a virtual path against the local view and the remote
// lister callbacks. pkg/vfs builds one from the paired View+Overlay and
// injects its own resolve/listChildren functions.
type Resolve struct {
	view    *View
	overlay *Overlay
}

// NewResolve builds the resolver over the paired view+overlay state.
func NewResolve(view *View, overlay *Overlay) Resolve {
	return Resolve{view: view, overlay: overlay}
}

// CachedEntry returns the cached entry identity for path.
func (r Resolve) CachedEntry(path string) (drive.Entry, bool) {
	return r.view.entries.Get(vfstypes.CleanVirtualPath(path))
}

// CommitResolvedChildren caches every child of a freshly listed parent and
// returns the one matching name.
func (r Resolve) CommitResolvedChildren(parentPath, name string, entries []drive.Entry) (drive.Entry, bool) {
	parentPath = vfstypes.CleanVirtualPath(parentPath)
	var found drive.Entry
	var foundOK bool
	for _, child := range entries {
		childPath := vfstypes.JoinVirtualPath(parentPath, child.Name)
		r.view.mu.RLock()
		child = NewRuntime(r.view).ApplyLocalModTimeLocked(childPath, child)
		r.view.mu.RUnlock()
		r.view.entries.Set(childPath, child)
		if child.Name == name {
			found = child
			foundOK = true
		}
	}
	return found, foundOK
}

// IsUnavailable reports whether path is hidden by any overlay facet.
func (r Resolve) IsUnavailable(path string) bool {
	path = vfstypes.CleanVirtualPath(path)
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	return r.overlay.unavailableLocked(path)
}

// IsRecentLocalDir reports whether path is a recently created local dir.
func (r Resolve) IsRecentLocalDir(path string) bool {
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

// ResolveWithRuntime walks one path resolution: unavailable paths and cached
// entries short-circuit; otherwise the parent resolves, the parent dir is
// listed and the matched child committed into the view.
func ResolveWithRuntime(ctx context.Context, path string, runtime resolver, resolveParent func(context.Context, string) (drive.Entry, error), listChildren func(context.Context, string, string) ([]drive.Entry, error)) (drive.Entry, error) {
	path = vfstypes.CleanVirtualPath(path)
	if runtime.IsUnavailable(path) {
		return drive.Entry{}, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if entry, ok := runtime.CachedEntry(path); ok {
		return entry, nil
	}
	name, parentPath := vfstypes.SplitVirtualPath(path)
	parent, err := resolveParent(ctx, parentPath)
	if err != nil {
		return drive.Entry{}, err
	}
	if runtime.IsRecentLocalDir(parentPath) {
		return drive.Entry{}, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	entries, err := listChildren(ctx, parentPath, parent.ID)
	if err != nil {
		return drive.Entry{}, err
	}
	if child, ok := runtime.CommitResolvedChildren(parentPath, name, entries); ok {
		return child, nil
	}
	return drive.Entry{}, fmt.Errorf("%w: %s", ErrNotFound, path)
}
