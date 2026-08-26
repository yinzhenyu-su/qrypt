package vfs

import (
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
)

// newVFSVisibilityRuntime builds the composite-domain sync surface for VFS
// adapters over the paired view/overlay/tasks state.
func newVFSVisibilityRuntime(v *VFS) view.Visibility {
	return view.NewVisibility(v.view.Overlay(), v.deletes.tasks, v.view, v.lister)
}

func (v *VFS) unhideCopyChild(parentPath, name string) {
	newVFSVisibilityRuntime(v).UnhideCopyChild(parentPath, name)
}

func (v *VFS) restoreDeletedPath(path string) (drive.Entry, bool) {
	return newVFSVisibilityRuntime(v).RestoreDeletedPath(path)
}

func (v *VFS) restoreDeletedAncestor(path string) {
	newVFSVisibilityRuntime(v).RestoreDeletedAncestor(path)
}

func (v *VFS) cancelDeletedFile(path string) {
	newVFSVisibilityRuntime(v).CancelDeletedFile(path)
}

func (v *VFS) isUnderRestoredDir(path string) bool {
	return newVFSVisibilityRuntime(v).IsUnderRestoredDir(path)
}
