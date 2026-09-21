package vfs

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
)

// TestRenameOverExistingDropsTargetListing: a rename that lands on an existing
// directory must not keep serving that directory's cached listing, which still
// belongs to the object the rename replaced.
func TestRenameOverExistingDropsTargetListing(t *testing.T) {
	t.Parallel()
	fs := newViewCommitVFS(t)
	rt := view.NewRuntime(fs.view)
	committer := newVFSViewCommitter(fs)

	rt.CommitChildren("/target", []drive.Entry{{ID: "old-child", ParentID: "target", Name: "y.txt"}}, time.Now().Add(time.Minute))
	if _, ok := rt.FreshList("/target", time.Now()); !ok {
		t.Fatal("target listing was not cached, test cannot observe the invalidation")
	}

	committer.CommitRemoteRename("/old", "/target", drive.Entry{ID: "target", ParentID: "/", Name: "target", IsDir: true})

	if _, ok := rt.FreshList("/target", time.Now()); ok {
		t.Fatal("replaced target directory still served a cached listing")
	}
}

// TestRenameMovesLocalDirectoryMarker: a directory created through the mount
// keeps its recent-local-dir marker at the path it was renamed to, so its
// children keep resolving locally until the backend catches up.
func TestRenameMovesLocalDirectoryMarker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newViewCommitVFS(t)
	rt := view.NewRuntime(fs.view)

	if _, err := fs.Mkdir(ctx, "/created"); err != nil {
		t.Fatal(err)
	}
	if !rt.IsRecentLocalDir("/created") {
		t.Fatal("created directory is not marked as a recent local directory")
	}
	if err := fs.Rename(ctx, "/created", "/moved"); err != nil {
		t.Fatal(err)
	}
	if !rt.IsRecentLocalDir("/moved") {
		t.Fatal("renamed directory lost its recent-local-dir marker")
	}
	if rt.IsRecentLocalDir("/created") {
		t.Fatal("pre-rename path kept a recent-local-dir marker")
	}
}
