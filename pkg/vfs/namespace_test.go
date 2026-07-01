package vfs_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/internal/driver/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestNamespaceRoutesByFirstPathSegment(t *testing.T) {
	ctx := context.Background()
	remoteA := t.TempDir()
	remoteB := t.TempDir()
	fsA, err := vfs.New(localfs.New(remoteA), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "a"), UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fsB, err := vfs.New(localfs.New(remoteB), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "b"), UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{
		{Name: "quark", FS: fsA},
		{Name: "localfs", FS: fsB},
	})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start(ctx)

	entries, err := ns.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "localfs" || entries[1].Name != "quark" {
		t.Fatalf("unexpected namespace root entries: %+v", entries)
	}
	for _, entry := range entries {
		if entry.ParentID != "/" {
			t.Fatalf("namespace mount parent id = %q, want / for %+v", entry.ParentID, entry)
		}
	}

	if _, err := ns.WriteAt(ctx, "/quark/a.txt", []byte("from quark"), 0); err != nil {
		t.Fatal(err)
	}
	if err := ns.Flush(ctx, "/quark/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := ns.WriteAt(ctx, "/localfs/a.txt", []byte("from localfs"), 0); err != nil {
		t.Fatal(err)
	}
	if err := ns.Flush(ctx, "/localfs/a.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, ns)

	gotA, err := os.ReadFile(filepath.Join(remoteA, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotA) != "from quark" {
		t.Fatalf("unexpected quark content: %q", gotA)
	}
	gotB, err := os.ReadFile(filepath.Join(remoteB, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotB) != "from localfs" {
		t.Fatalf("unexpected localfs content: %q", gotB)
	}

	rc, err := ns.Read(ctx, "/localfs/a.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	readBack, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(readBack) != "from localfs" {
		t.Fatalf("unexpected readback: %q", readBack)
	}
}

func TestNamespaceRejectsCrossMountRename(t *testing.T) {
	ctx := context.Background()
	fsA, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "a")})
	if err != nil {
		t.Fatal(err)
	}
	fsB, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "b")})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "a", FS: fsA}, {Name: "b", FS: fsB}})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start(ctx)
	if _, err := ns.WriteAt(ctx, "/a/file.txt", []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := ns.Rename(ctx, "/a/file.txt", "/b/file.txt"); err == nil {
		t.Fatal("expected cross-mount rename to fail")
	}
}

func TestNamespaceSpaceAggregatesMounts(t *testing.T) {
	ctx := context.Background()
	fsA, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "a")})
	if err != nil {
		t.Fatal(err)
	}
	fsB, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "b")})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "a", FS: fsA}, {Name: "b", FS: fsB}})
	if err != nil {
		t.Fatal(err)
	}

	spaceA, err := fsA.Space(ctx)
	if err != nil {
		t.Fatal(err)
	}
	spaceB, err := fsB.Space(ctx)
	if err != nil {
		t.Fatal(err)
	}
	space, err := ns.Space(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if space.Total != spaceA.Total+spaceB.Total {
		t.Fatalf("total space = %d, want %d", space.Total, spaceA.Total+spaceB.Total)
	}
	if space.Free != spaceA.Free+spaceB.Free {
		t.Fatalf("free space = %d, want %d", space.Free, spaceA.Free+spaceB.Free)
	}
}

func TestNamespaceMarksOnlyNamespaceRootReadOnly(t *testing.T) {
	ctx := context.Background()
	fsA, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "a")})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "a", FS: fsA}})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start(ctx)

	if !ns.IsReadOnlyPath("/") {
		t.Fatal("expected namespace root to be read-only")
	}
	for _, path := range []string{"/a", "/new", "/a/file.txt"} {
		if ns.IsReadOnlyPath(path) {
			t.Fatalf("expected %s to be shown writable to FUSE", path)
		}
	}
	if _, err := ns.Mkdir(ctx, "/a"); err != vfs.ErrReadOnly {
		t.Fatalf("Mkdir mount root err = %v, want ErrReadOnly", err)
	}
}

func TestNamespaceVirtualDirectoryModTimeIsStable(t *testing.T) {
	ctx := context.Background()
	fsA, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "a")})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "a", FS: fsA}})
	if err != nil {
		t.Fatal(err)
	}

	rootA, err := ns.Stat(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	mountA, err := ns.Stat(ctx, "/a")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	rootB, err := ns.Stat(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	mountB, err := ns.Stat(ctx, "/a")
	if err != nil {
		t.Fatal(err)
	}
	if !rootA.ModTime.Equal(rootB.ModTime) {
		t.Fatalf("namespace root modtime changed from %s to %s", rootA.ModTime, rootB.ModTime)
	}
	if !mountA.ModTime.Equal(mountB.ModTime) {
		t.Fatalf("namespace mount modtime changed from %s to %s", mountA.ModTime, mountB.ModTime)
	}
	if mountA.ParentID != "/" {
		t.Fatalf("namespace mount parent id = %q, want /", mountA.ParentID)
	}
}
