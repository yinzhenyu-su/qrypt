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
	if !info.CanUpload || !info.CanMkdir || !info.CanUploadChild || !info.CanMkdirChild {
		t.Fatalf("root create capabilities = %+v, want upload and mkdir", info)
	}
	if !info.CanRenameChild || !info.CanMoveChild || !info.CanRemoveChild {
		t.Fatalf("root child mutation capabilities = %+v, want child mutations enabled", info)
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
	if !mountRoot.CanUpload || !mountRoot.CanMkdir || !mountRoot.CanUploadChild || !mountRoot.CanMkdirChild {
		t.Fatalf("mount root create capabilities = %+v, want upload and mkdir", mountRoot)
	}
	if !mountRoot.CanRenameChild || !mountRoot.CanMoveChild || !mountRoot.CanRemoveChild {
		t.Fatalf("mount root child mutation capabilities = %+v, want child mutations enabled", mountRoot)
	}
	if mountRoot.CanRemove || mountRoot.CanRename || mountRoot.CanMove {
		t.Fatalf("mount root target mutation capabilities = %+v, want target mutations disabled", mountRoot)
	}

	dir, err := ns.CapabilitiesForPath(ctx, "/local/dir")
	if err != nil {
		t.Fatal(err)
	}
	if dir.MountRoot || !dir.CanList || dir.CanRead || !dir.CanUpload || !dir.CanMkdir || !dir.CanRemove || !dir.CanRename || !dir.CanMove || !dir.CanRemoveChild || !dir.CanRenameChild {
		t.Fatalf("directory capabilities = %+v", dir)
	}

	file, err := ns.CapabilitiesForPath(ctx, "/local/dir/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !file.CanRead || file.CanList || file.CanUpload || file.CanMkdir || !file.CanRemove || !file.CanRename || !file.CanMove || file.CanRemoveChild || file.CanRenameChild {
		t.Fatalf("file capabilities = %+v", file)
	}
}

func TestNamespaceMountsReportRuntimePathsAndEncryption(t *testing.T) {
	plain, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{Name: "plain", CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{Name: "secret", CacheDir: t.TempDir(), Encrypted: true})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "secret", FS: secret}, {Name: "plain", FS: plain}})
	if err != nil {
		t.Fatal(err)
	}

	mounts := ns.Mounts()
	if len(mounts) != 2 {
		t.Fatalf("mounts = %+v, want two mounts", mounts)
	}
	if mounts[0].Path != "/plain" || mounts[0].Encrypted {
		t.Fatalf("first mount = %+v, want plain unencrypted", mounts[0])
	}
	if mounts[1].Path != "/secret" || !mounts[1].Encrypted {
		t.Fatalf("second mount = %+v, want secret encrypted", mounts[1])
	}
}
