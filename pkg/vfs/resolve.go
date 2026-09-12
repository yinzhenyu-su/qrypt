package vfs

import (
	"context"
	"errors"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
)

func (v *VFS) parent(ctx context.Context, path string) (drive.Entry, string, error) {
	path = vfstypes.CleanVirtualPath(path)
	name, parentPath := vfstypes.SplitVirtualPath(path)
	parent, err := v.resolve(ctx, parentPath)
	return parent, name, err
}

func (v *VFS) resolve(ctx context.Context, path string) (drive.Entry, error) {
	runtime := view.NewResolve(v.view, v.view.Overlay())
	return view.ResolveWithRuntime(ctx, path, runtime, v.resolve, v.listChildren)
}

// remoteEntry reports the backend object at path, filtering out the local
// pending projection: a path that only shows up as its own pending upload
// (the synthesized entry carries the staging FID as its ID) has no backend
// object yet. It lists the parent directly rather than resolving the child,
// so a path inside a recently created directory still sees the backend truth.
func (v *VFS) remoteEntry(ctx context.Context, path string) (drive.Entry, bool, error) {
	path = vfstypes.CleanVirtualPath(path)
	name, parentPath := vfstypes.SplitVirtualPath(path)
	parent, err := v.resolve(ctx, parentPath)
	if err != nil {
		// An unresolvable parent cannot hold a backend object at path: it may
		// exist only locally, or not at all yet.
		return drive.Entry{}, false, nil
	}
	entries, err := v.listChildren(ctx, parentPath, parent.ID)
	if err != nil {
		if errors.Is(err, drive.ErrNotFound) {
			// The backend has not caught up with the directory yet; there is
			// no reachable object at path.
			return drive.Entry{}, false, nil
		}
		return drive.Entry{}, false, err
	}
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}
		if _, pending := v.uploads.Store().PendingByID(entry.ID); pending {
			return drive.Entry{}, false, nil
		}
		return entry, true, nil
	}
	return drive.Entry{}, false, nil
}
