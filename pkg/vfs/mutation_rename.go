package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/mutation"
)

func (v *VFS) renameWithDeps(ctx context.Context, oldPath, newPath string, backend mutation.Backend, runtime mutationRuntime, committer viewCommitter) (err error) {
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

// pathResolver is the narrow resolution boundary used by the mutation
// adapters: it resolves a virtual path to its entry and splits a path into
// parent entry and child name. *VFS satisfies it via its package-private
// resolve/parent methods.
type pathResolver interface {
	resolve(ctx context.Context, path string) (drive.Entry, error)
	parent(ctx context.Context, path string) (drive.Entry, string, error)
}

// vfsRenameResolver adapts VFS resolution to mutation.Resolver.

type vfsRenameResolver struct{ resolver pathResolver }

func newVFSRenameResolver(v *VFS) vfsRenameResolver { return vfsRenameResolver{resolver: v} }

func (r vfsRenameResolver) Resolve(ctx context.Context, path string) (drive.Entry, error) {
	return r.resolver.resolve(ctx, path)
}

func (r vfsRenameResolver) Parent(ctx context.Context, path string) (drive.Entry, string, error) {
	return r.resolver.parent(ctx, path)
}

// vfsRenamePending adapts the pending-upload rename to mutation.PendingRenamer.

type vfsRenamePending struct{ v *VFS }

func newVFSRenamePending(v *VFS) vfsRenamePending { return vfsRenamePending{v: v} }

func (r vfsRenamePending) IsPending(path string) bool {
	_, err := r.v.pendingUpload(path)
	return err == nil
}

func (r vfsRenamePending) RenamePending(ctx context.Context, oldPath, newPath string, parent drive.Entry, name string) error {
	// Take the path lock FIRST and re-read the pending inside it: a pending
	// read before the lock could be stale by the time we mutate it (e.g. the
	// frozen generation was rotated), committing an outdated FID/LocalPath.
	unlockOld := r.v.lockPath(oldPath)
	defer unlockOld()
	pending, err := r.v.pendingUpload(oldPath)
	if err != nil {
		return err
	}
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
