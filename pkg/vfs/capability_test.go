package vfs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestVFSRootCapabilitiesAllowCreatingChildren(t *testing.T) {
	ctx := context.Background()
	fs, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	info, err := fs.CapabilitiesForPath(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if info.Root || info.MountRoot || !info.CanList || info.CanRead {
		t.Fatalf("root info = %+v", info)
	}
	if !info.CanUpload || !info.CanMkdir {
		t.Fatalf("root create capabilities = %+v, want upload and mkdir", info)
	}
	if info.CanRemove || info.CanRename || info.CanMove {
		t.Fatalf("root target mutation capabilities = %+v, want target mutations disabled", info)
	}
}

func TestNamespaceCapabilitiesRespectRootAndMountRoot(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.Mkdir(filepath.Join(remote, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "dir", "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "local", FS: fs}})
	if err != nil {
		t.Fatal(err)
	}

	root, err := ns.CapabilitiesForPath(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if !root.Root || !root.CanList || root.CanUpload || root.CanMkdir || root.CanRemove {
		t.Fatalf("namespace root capabilities = %+v", root)
	}

	mountRoot, err := ns.CapabilitiesForPath(ctx, "/local")
	if err != nil {
		t.Fatal(err)
	}
	if mountRoot.Mount != "local" || !mountRoot.MountRoot || !mountRoot.CanList {
		t.Fatalf("mount root capabilities = %+v", mountRoot)
	}
	if !mountRoot.CanUpload || !mountRoot.CanMkdir {
		t.Fatalf("mount root create capabilities = %+v, want upload and mkdir", mountRoot)
	}
	if mountRoot.CanRemove || mountRoot.CanRename || mountRoot.CanMove {
		t.Fatalf("mount root target mutation capabilities = %+v, want target mutations disabled", mountRoot)
	}

	dir, err := ns.CapabilitiesForPath(ctx, "/local/dir")
	if err != nil {
		t.Fatal(err)
	}
	if dir.MountRoot || !dir.CanList || dir.CanRead || !dir.CanUpload || !dir.CanMkdir || !dir.CanRemove || !dir.CanRename || !dir.CanMove {
		t.Fatalf("directory capabilities = %+v", dir)
	}

	file, err := ns.CapabilitiesForPath(ctx, "/local/dir/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !file.CanRead || file.CanList || file.CanUpload || file.CanMkdir || !file.CanRemove || !file.CanRename || !file.CanMove {
		t.Fatalf("file capabilities = %+v", file)
	}
}
