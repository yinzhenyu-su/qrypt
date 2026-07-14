package vfs_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/internal/driver/localfs"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type fixedSpaceDriver struct {
	drive.UnsupportedOperations
	space drive.Space
}

func (d fixedSpaceDriver) Init(context.Context) error { return nil }
func (d fixedSpaceDriver) Drop(context.Context) error { return nil }
func (d fixedSpaceDriver) List(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}
func (d fixedSpaceDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d fixedSpaceDriver) Space(context.Context) (drive.Space, error) {
	return d.space, nil
}
func (d fixedSpaceDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilitySpace}
}
func (d fixedSpaceDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{Driver: "fixed-space", Health: drive.HealthLevelOK, GeneratedAt: time.Now()}, nil
}
func (d fixedSpaceDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

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

func TestNamespaceStartDirectoryPrefetchWarmsAllMounts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drvA := &treeListDriver{
		lists: map[string]int{},
		entries: map[string][]drive.Entry{
			"0":     {{ID: "a-dir", ParentID: "0", Name: "warm", IsDir: true}},
			"a-dir": {{ID: "a-file", ParentID: "a-dir", Name: "a.txt"}},
		},
	}
	drvB := &treeListDriver{
		lists: map[string]int{},
		entries: map[string][]drive.Entry{
			"0":     {{ID: "b-dir", ParentID: "0", Name: "warm", IsDir: true}},
			"b-dir": {{ID: "b-file", ParentID: "b-dir", Name: "b.txt"}},
		},
	}
	fsA, err := vfs.New(drvA, vfs.Options{CacheDir: filepath.Join(t.TempDir(), "a")})
	if err != nil {
		t.Fatal(err)
	}
	fsB, err := vfs.New(drvB, vfs.Options{CacheDir: filepath.Join(t.TempDir(), "b")})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "a", FS: fsA}, {Name: "b", FS: fsB}})
	if err != nil {
		t.Fatal(err)
	}

	ns.StartDirectoryPrefetch(ctx)

	waitForCondition(t, func() bool {
		return drvA.listCount("0") == 1 && drvA.listCount("a-dir") == 1 &&
			drvB.listCount("0") == 1 && drvB.listCount("b-dir") == 1
	})
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
	spaceA := drive.Space{Total: 1000, Free: 700}
	spaceB := drive.Space{Total: 2000, Free: 300}
	fsA, err := vfs.New(fixedSpaceDriver{space: spaceA}, vfs.Options{CacheDir: filepath.Join(t.TempDir(), "a")})
	if err != nil {
		t.Fatal(err)
	}
	fsB, err := vfs.New(fixedSpaceDriver{space: spaceB}, vfs.Options{CacheDir: filepath.Join(t.TempDir(), "b")})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "a", FS: fsA}, {Name: "b", FS: fsB}})
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
