package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/mutation"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
	"time"
)

func (v *VFS) prepareDirectoryCopyWithRuntime(ctx context.Context, path string, runtime vfsDirectoryCopyRuntime) error {
	path = vfstypes.CleanVirtualPath(path)
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	if !entry.IsDir {
		return fmt.Errorf("vfs: %s is not a directory", path)
	}
	hideNames := map[string]time.Time{}
	if entries, err := runtime.ListChildren(ctx, entry.ID); err == nil {
		expires := time.Now().Add(view.DirectoryCopyHideTTL)
		for _, child := range entries {
			if !view.IsAppleMetadataName(child.Name) {
				hideNames[child.Name] = expires
			}
		}
	}
	if err := runtime.CleanupPendingChildren(path); err != nil {
		return err
	}
	runtime.PrepareLocalDirectoryCopy(path, hideNames)
	return nil
}

func (v *VFS) mkdirWithDeps(ctx context.Context, path string, backend mutation.Backend, committer viewCommitter, cache listedChildrenCache) (entry drive.Entry, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpMkdir, err) }()
	if err := newVFSDriverRuntime(v.driver, v.testEnabled).RequireCapability(drive.CapabilityWriter, "mkdir"); err != nil {
		return drive.Entry{}, err
	}
	coordinator := mutation.NewMkdirCoordinator(
		newVFSRenameResolver(v),
		backend,
		newVFSMkdirView(v, committer, cache),
	)
	return coordinator.Mkdir(ctx, path)
}

// vfsMkdirView adapts VFS overlay restoration + view commits to
// mutation.MkdirView. driverMutationBackend already satisfies
// mutation.MkdirRemote (List/Mkdir); vfsRenameResolver satisfies
// mutation.MkdirResolver (Resolve/Parent).

type vfsMkdirView struct {
	committer          viewCommitter
	cache              listedChildrenCache
	restoreDeleted     func(string) (drive.Entry, bool)
	restoreAncestor    func(string)
	isUnderRestoredDir func(string) bool
}

func newVFSMkdirView(v *VFS, committer viewCommitter, cache listedChildrenCache) vfsMkdirView {
	return vfsMkdirView{
		committer:          committer,
		cache:              cache,
		restoreDeleted:     v.restoreDeletedPath,
		restoreAncestor:    v.restoreDeletedAncestor,
		isUnderRestoredDir: v.isUnderRestoredDir,
	}
}

func (r vfsMkdirView) RestoreDeleted(path string) (drive.Entry, bool) {
	return r.restoreDeleted(path)
}

func (r vfsMkdirView) RestoreDeletedAncestor(path string) {
	r.restoreAncestor(path)
}

func (r vfsMkdirView) IsUnderRestoredDir(path string) bool {
	return r.isUnderRestoredDir(path)
}

func (r vfsMkdirView) CommitMkdir(path string, entry drive.Entry) {
	r.committer.CommitMkdir(path, entry)
}

func (r vfsMkdirView) CacheListedChildren(parentPath string, entries []drive.Entry) {
	r.cache.CacheListedChildren(parentPath, entries)
}

// Compile-time assertions for the mkdir adapters.
var _ mutation.MkdirResolver = vfsRenameResolver{}
var _ mutation.MkdirView = vfsMkdirView{}

type vfsDirectoryCopyRuntime struct {
	driver  drive.Driver
	store   *uploadStore
	uploads *uploadService
	hashes  *upload.HashTracker
	view    *view.View
}

func newVFSDirectoryCopyRuntime(v *VFS) vfsDirectoryCopyRuntime {
	return vfsDirectoryCopyRuntime{
		driver:  v.driver,
		store:   v.uploads.Store(),
		uploads: v.uploads,
		hashes:  v.hashes,
		view:    v.view,
	}
}

func (r vfsDirectoryCopyRuntime) ListChildren(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return r.driver.List(ctx, parentID)
}

func (r vfsDirectoryCopyRuntime) CleanupPendingChildren(path string) error {
	// Durable store commit first; timer/hash cleanup runs only after it
	// succeeds, so a failed commit never leaves the children's timers
	// cancelled while their pendings are still visible.
	if err := r.store.RemoveUploadsUnder(path); err != nil {
		return err
	}
	r.uploads.CancelChildUploads(path)
	r.hashes.RemoveUnder(path)
	return nil
}

func (r vfsDirectoryCopyRuntime) PrepareLocalDirectoryCopy(path string, hideNames map[string]time.Time) {
	view.NewRuntime(r.view).PrepareLocalDirectoryCopy(path, hideNames)
}
