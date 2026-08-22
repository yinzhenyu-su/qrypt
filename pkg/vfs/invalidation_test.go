package vfs

import (
	"reflect"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSInvalidationSubscription(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	var paths []string
	unsubscribe := fs.SubscribeInvalidations(func(path string) {
		paths = append(paths, path)
	})
	fs.emitInvalidation("dir/../file.txt")
	if want := []string{"/file.txt"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("invalidation paths = %v, want %v", paths, want)
	}

	unsubscribe()
	unsubscribe()
	fs.emitInvalidation("/ignored.txt")
	if want := []string{"/file.txt"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths after unsubscribe = %v, want %v", paths, want)
	}
}

func TestNamespaceInvalidationPrefixesMountName(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })
	ns, err := NewNamespace([]Mount{{Name: "quark", FS: fs}})
	if err != nil {
		t.Fatal(err)
	}

	var paths []string
	unsubscribe := ns.SubscribeInvalidations(func(path string) {
		paths = append(paths, path)
	})
	fs.emitInvalidation("/videos/movie.bin")
	if want := []string{"/quark/videos/movie.bin"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("namespace invalidation paths = %v, want %v", paths, want)
	}

	unsubscribe()
	fs.emitInvalidation("/ignored.bin")
	if want := []string{"/quark/videos/movie.bin"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths after unsubscribe = %v, want %v", paths, want)
	}
}

func TestInvalidationListenerPanicDoesNotBlockOtherListeners(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })
	fs.SubscribeInvalidations(func(string) { panic("boom") })

	called := false
	fs.SubscribeInvalidations(func(string) { called = true })
	fs.emitInvalidation("/file.txt")
	if !called {
		t.Fatal("listener after panicking listener was not called")
	}
}
