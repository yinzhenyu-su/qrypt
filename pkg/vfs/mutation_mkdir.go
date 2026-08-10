package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/vfs/mutation"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"path/filepath"
	"time"
)

func (v *VFS) prepareDirectoryCopyWithRuntime(ctx context.Context, path string, runtime vfsDirectoryCopyRuntime) error {
	path = cleanVirtual(path)
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	if !entry.IsDir {
		return fmt.Errorf("vfs: %s is not a directory", path)
	}
	hideNames := map[string]time.Time{}
	if entries, err := runtime.ListChildren(ctx, entry.ID); err == nil {
		expires := time.Now().Add(directoryCopyHideTTL)
		for _, child := range entries {
			if !isAppleMetadataName(child.Name) {
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

func (v *VFS) mkdirWithDeps(ctx context.Context, path string, backend mutationBackend, committer viewCommitter, cache listedChildrenCache) (entry drive.Entry, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpMkdir, err) }()
	if err := newVFSDriverRuntime(v).RequireCapability(drive.CapabilityWriter, "mkdir"); err != nil {
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
	v         *VFS
	committer viewCommitter
	cache     listedChildrenCache
}

func newVFSMkdirView(v *VFS, committer viewCommitter, cache listedChildrenCache) vfsMkdirView {
	return vfsMkdirView{v: v, committer: committer, cache: cache}
}

func (r vfsMkdirView) RestoreDeleted(path string) (drive.Entry, bool) {
	return r.v.restoreDeletedPath(path)
}

func (r vfsMkdirView) RestoreDeletedAncestor(path string) {
	r.v.restoreDeletedAncestor(path)
}

func (r vfsMkdirView) IsUnderRestoredDir(path string) bool {
	return r.v.isUnderRestoredDir(path)
}

func (r vfsMkdirView) CommitMkdir(path string, entry drive.Entry) {
	r.committer.CommitMkdir(path, entry)
}

func (r vfsMkdirView) CacheListedChildren(parentPath string, entries []drive.Entry) {
	r.cache.CacheListedChildren(parentPath, entries)
}

// Compile-time assertions for the mkdir adapters.
var _ mutation.MkdirRemote = driverMutationBackend{}
var _ mutation.MkdirResolver = vfsRenameResolver{}
var _ mutation.MkdirView = vfsMkdirView{}

type vfsDirectoryCopyRuntime struct {
	v *VFS
}

func newVFSDirectoryCopyRuntime(v *VFS) vfsDirectoryCopyRuntime {
	return vfsDirectoryCopyRuntime{v: v}
}

func (r vfsDirectoryCopyRuntime) ListChildren(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return newVFSDriverRuntime(r.v).List(ctx, parentID)
}

func (r vfsDirectoryCopyRuntime) CleanupPendingChildren(path string) error {
	// Durable store commit first; timer/hash cleanup runs only after it
	// succeeds, so a failed commit never leaves the children's timers
	// cancelled while their pendings are still visible.
	if err := r.v.uploads.Store().RemoveUploadsUnder(path); err != nil {
		return err
	}
	r.v.uploads.CancelChildUploads(path)
	r.v.hashes.removeUnder(path)
	return nil
}

func (r vfsDirectoryCopyRuntime) PrepareLocalDirectoryCopy(path string, hideNames map[string]time.Time) {
	r.v.view.entries.Range(func(cachedPath string, cachedEntry drive.Entry) bool {
		if filepath.Dir(cachedPath) == path {
			if _, ok := hideNames[cachedEntry.Name]; !ok && !isAppleMetadataName(cachedEntry.Name) {
				hideNames[cachedEntry.Name] = time.Now().Add(directoryCopyHideTTL)
			}
			r.v.view.entries.Delete(cachedPath)
		}
		return true
	})
	r.v.view.mu.Lock()
	r.v.markLocalDirLocked(path)
	r.v.invalidateListLocked(path)
	r.v.view.mu.Unlock()
	r.v.setCopyHidden(path, hideNames)
}
