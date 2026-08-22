package vfs

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// A second frozen generation replaces the first generation's debounce
// timer. The displaced timer can no longer reach a worker, so scheduling the
// replacement must retire its now-unreferenced staging file immediately.
func TestFlushRetiresGenerationDisplacedBeforeTimerFire(t *testing.T) {
	fs, err := New(drive.NewFakeDriver(), Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close(context.Background()) }()

	ctx := context.Background()
	if err := fs.Create(ctx, "/file.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/file.txt", []byte("one"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/file.txt"); err != nil {
		t.Fatal(err)
	}
	first, ok := fs.uploads.Store().UploadByPath("/file.txt")
	if !ok {
		t.Fatal("first generation missing")
	}

	if _, err := fs.WriteAt(ctx, "/file.txt", []byte("two"), 0); err != nil {
		t.Fatal(err)
	}
	second, ok := fs.uploads.Store().UploadByPath("/file.txt")
	if !ok || second.FID == first.FID {
		t.Fatalf("second generation = %+v, first = %+v", second, first)
	}
	if err := fs.Flush(ctx, "/file.txt"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(first.LocalPath); !os.IsNotExist(err) {
		t.Fatalf("displaced staging still exists, err=%v", err)
	}
	if _, err := os.Stat(second.LocalPath); err != nil {
		t.Fatalf("current staging removed: %v", err)
	}
}
