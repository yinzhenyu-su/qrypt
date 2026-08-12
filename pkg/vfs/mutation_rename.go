package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/mutation"
)

func (v *VFS) renameWithDeps(ctx context.Context, oldPath, newPath string, backend mutationBackend, runtime mutationRuntime, committer viewCommitter) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpRename, err) }()
	if err := newVFSDriverRuntime(v).RequireCapability(drive.CapabilityWriter, "rename"); err != nil {
		return err
	}
	coordinator := mutation.NewCoordinator(
		newVFSRenameResolver(v),
		newVFSRenamePending(v),
		newVFSRenameView(committer, runtime),
		mutation.NewRemoteRenamer(backend),
	)
	return coordinator.Rename(ctx, oldPath, newPath)
}

// vfsRenameResolver adapts VFS resolution to mutation.Resolver.

type vfsRenameResolver struct{ v *VFS }

func newVFSRenameResolver(v *VFS) vfsRenameResolver { return vfsRenameResolver{v: v} }

func (r vfsRenameResolver) Resolve(ctx context.Context, path string) (drive.Entry, error) {
	return r.v.resolve(ctx, path)
}

func (r vfsRenameResolver) Parent(ctx context.Context, path string) (drive.Entry, string, error) {
	return r.v.parent(ctx, path)
}

// vfsRenamePending adapts the pending-upload rename to mutation.PendingRenamer.

type vfsRenamePending struct{ v *VFS }

func newVFSRenamePending(v *VFS) vfsRenamePending { return vfsRenamePending{v: v} }

func (r vfsRenamePending) IsPending(path string) bool {
	_, err := r.v.pendingUpload(path)
	return err == nil
}

func (r vfsRenamePending) RenamePending(ctx context.Context, oldPath, newPath string, parent drive.Entry, name string) error {
	pending, err := r.v.pendingUpload(oldPath)
	if err != nil {
		return err
	}
	unlockOld := r.v.lockPath(oldPath)
	defer unlockOld()
	pending.Path = newPath
	pending.ParentID = parent.ID
	pending.Name = name
	return newVFSMutationRuntime(r.v).RenamePendingUpload(oldPath, newPath, pending)
}

// vfsRenameView adapts the rename view commit; read-cache invalidation
// stays on the rename-time runtime, the view commit on the committer.

type vfsRenameView struct {
	committer viewCommitter
	runtime   mutationRuntime
}

func newVFSRenameView(committer viewCommitter, runtime mutationRuntime) vfsRenameView {
	return vfsRenameView{committer: committer, runtime: runtime}
}

func (r vfsRenameView) InvalidateReadCache(entry drive.Entry) {
	r.runtime.InvalidateReadCache(entry)
}

func (r vfsRenameView) CommitRemoteRename(oldPath, newPath string, entry drive.Entry) {
	r.committer.CommitRemoteRename(oldPath, newPath, entry)
}
