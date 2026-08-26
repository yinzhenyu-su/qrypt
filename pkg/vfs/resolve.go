package vfs

import (
	"context"

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
