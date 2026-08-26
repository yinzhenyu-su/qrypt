package vfs

import (
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
)

// VFS forwards for the view domain: the runtime operations the namespace and
// write paths use. All view state lives in the view package; these thins
// adapt to it without widening the VFS surface.

func (v *VFS) RefreshPath(path string) {
	view.NewRuntime(v.view).RefreshPath(path)
}

func (v *VFS) applyLocalModTime(path string, entry drive.Entry) drive.Entry {
	return view.NewRuntime(v.view).ApplyLocalModTime(path, entry)
}

func (v *VFS) setLocalModTime(path string, modTime time.Time) {
	view.NewRuntime(v.view).SetLocalModTime(path, modTime)
}
