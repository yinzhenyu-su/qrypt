package vfs

import (
	"reflect"
	"sync"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSInvalidationSubscription(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestNamespaceInvalidationConcurrentSubscriptions(t *testing.T) {
	t.Parallel()
	ns, err := NewNamespace(nil)
	if err != nil {
		t.Fatal(err)
	}

	const subscriberCount = 64
	start := make(chan struct{})
	unsubscribes := make([]func(), subscriberCount)
	var wg sync.WaitGroup
	for i := range subscriberCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			unsubscribes[i] = ns.SubscribeInvalidations(func(string) {})
		}()
	}
	close(start)
	wg.Wait()

	ns.mu.RLock()
	got := len(ns.subs)
	ns.mu.RUnlock()
	if got != subscriberCount {
		t.Fatalf("subscriptions = %d, want %d", got, subscriberCount)
	}
	for _, unsubscribe := range unsubscribes {
		unsubscribe()
	}
}

func TestInvalidationListenerPanicDoesNotBlockOtherListeners(t *testing.T) {
	t.Parallel()
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
