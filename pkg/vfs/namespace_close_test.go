package vfs

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestNamespaceCloseShortContextStillTearsDownAllMounts guards the
// concurrent Close: with an already-expired context, Close returns
// ctx.Err() immediately, but every mount's teardown must still run to
// completion instead of being starved by a serialized budget.
func TestNamespaceCloseShortContextStillTearsDownAllMounts(t *testing.T) {
	mounts := make([]*VFS, 4)
	nsMounts := make([]Mount, len(mounts))
	for i := range mounts {
		fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		fs.Start(context.Background())
		mounts[i] = fs
		nsMounts[i] = Mount{Name: "m" + string(rune('0'+i)), FS: fs}
	}
	ns, err := NewNamespace(nsMounts)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done() // ensure the budget is already exhausted
	if err := ns.Close(ctx); err == nil {
		t.Fatal("expected ctx error from Close")
	}

	deadline := time.Now().Add(5 * time.Second)
	for _, fs := range mounts {
		for {
			fs.lifecycleMu.Lock()
			state := fs.lifecycle
			fs.lifecycleMu.Unlock()
			if state == lifecycleClosed {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("mount teardown did not finish after Close returned")
			}
			time.Sleep(time.Millisecond)
		}
	}
}
