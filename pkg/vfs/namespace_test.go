package vfs_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/internal/driver/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestNamespaceRoutesByFirstPathSegment(t *testing.T) {
	ctx := context.Background()
	remoteA := t.TempDir()
	remoteB := t.TempDir()
	fsA, err := vfs.New(localfs.New(remoteA), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "a")})
	if err != nil {
		t.Fatal(err)
	}
	fsB, err := vfs.New(localfs.New(remoteB), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "b")})
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

func TestNamespaceMarksRootAndMountNamesReadOnly(t *testing.T) {
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

	for _, path := range []string{"/", "/a", "/new"} {
		if !ns.IsReadOnlyPath(path) {
			t.Fatalf("expected %s to be read-only", path)
		}
	}
	if ns.IsReadOnlyPath("/a/file.txt") {
		t.Fatal("expected mounted content path to remain writable")
	}
	if _, err := ns.Mkdir(ctx, "/a"); err != vfs.ErrReadOnly {
		t.Fatalf("Mkdir mount root err = %v, want ErrReadOnly", err)
	}
}
