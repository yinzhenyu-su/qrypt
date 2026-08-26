package vfs

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func newTestVFSForNamespace(t *testing.T) *VFS {
	t.Helper()
	fs, err := New(localfs.New(t.TempDir()), Options{Name: "m", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })
	return fs
}

func TestNamespaceAddMountVisibleToOperations(t *testing.T) {
	a := newTestVFSForNamespace(t)
	b := newTestVFSForNamespace(t)
	ns, err := NewNamespace([]Mount{{Name: "a", FS: a}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ns.Stat(context.Background(), "/b"); !IsNotFound(err) {
		t.Fatalf("stat pre-add = %v, want not-found", err)
	}
	if err := ns.AddMount(Mount{Name: "b", FS: b}); err != nil {
		t.Fatal(err)
	}
	entry, err := ns.Stat(context.Background(), "/b")
	if err != nil {
		t.Fatalf("stat post-add: %v", err)
	}
	if entry.Name != "b" || !entry.IsDir {
		t.Fatalf("added mount entry = %+v", entry)
	}
}

func TestNamespaceAddMountRejectsBadMounts(t *testing.T) {
	fs := newTestVFSForNamespace(t)
	ns, err := NewNamespace([]Mount{{Name: "a", FS: fs}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ns.AddMount(Mount{Name: " a "}); err == nil {
		t.Fatal("empty mount name accepted")
	}
	if err := ns.AddMount(Mount{Name: "a", FS: fs}); err == nil {
		t.Fatal("duplicate mount name accepted")
	}
	if err := ns.AddMount(Mount{Name: "b"}); err == nil {
		t.Fatal("nil filesystem accepted")
	}
	// A Namespace satisfies MountedFileSystem but is not a VFS instance; the
	// aggregator rejects it at the assertion boundary.
	inner, err := NewNamespace([]Mount{{Name: "x", FS: fs}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ns.AddMount(Mount{Name: "b", FS: inner}); err == nil {
		t.Fatal("non-VFS filesystem accepted")
	}
}

func TestNamespaceRemoveMountDetaches(t *testing.T) {
	a := newTestVFSForNamespace(t)
	b := newTestVFSForNamespace(t)
	ns, err := NewNamespace([]Mount{{Name: "a", FS: a}, {Name: "b", FS: b}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ns.RemoveMount("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := ns.Stat(context.Background(), "/b"); !IsNotFound(err) {
		t.Fatalf("stat after remove = %v, want not-found", err)
	}
	if err := ns.RemoveMount("b"); err == nil {
		t.Fatal("removing unknown mount accepted")
	}
	if _, err := ns.Stat(context.Background(), "/a"); err != nil {
		t.Fatalf("sibling mount affected by remove: %v", err)
	}
}

func TestNamespaceDynamicMountInvalidationSubscription(t *testing.T) {
	a := newTestVFSForNamespace(t)
	b := newTestVFSForNamespace(t)
	ns, err := NewNamespace([]Mount{{Name: "a", FS: a}})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	unsubscribe := ns.SubscribeInvalidations(func(path string) {
		paths = append(paths, path)
	})

	if err := ns.AddMount(Mount{Name: "b", FS: b}); err != nil {
		t.Fatal(err)
	}
	b.emitInvalidation("/uploads/x.bin")
	if want := "/b/uploads/x.bin"; len(paths) != 1 || paths[0] != want {
		t.Fatalf("paths after add = %v, want [%s]", paths, want)
	}

	if err := ns.RemoveMount("b"); err != nil {
		t.Fatal(err)
	}
	b.emitInvalidation("/uploads/y.bin")
	if len(paths) != 1 {
		t.Fatalf("paths after remove = %v, want only the pre-remove path", paths)
	}

	unsubscribe()
	c := newTestVFSForNamespace(t)
	if err := ns.AddMount(Mount{Name: "c", FS: c}); err != nil {
		t.Fatal(err)
	}
	c.emitInvalidation("/uploads/z.bin")
	a.emitInvalidation("/after.txt")
	if len(paths) != 1 {
		t.Fatalf("paths after unsubscribe = %v, want no growth", paths)
	}
}
