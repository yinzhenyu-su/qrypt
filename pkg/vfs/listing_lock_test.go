package vfs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestListCommitRacesLocalModTimeUpdate guards the View lock boundary: a
// remote list commit applies local modtimes while holding the view lock
// (CommitList), racing concurrent SetModTime writes to the same map must
// be race-free. This would trip the race detector (concurrent map
// read/write) if the lister applied modtimes through an unlocked interface
// method.
func TestListCommitRacesLocalModTimeUpdate(t *testing.T) {
	drv := drive.NewFakeDriver()
	if err := drv.Seed(map[string]string{"a.txt": "content", "b.txt": "more"}); err != nil {
		t.Fatal(err)
	}
	fs, err := New(drv, Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   time.Hour, // never auto-upload; keep the list path warm
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.Start(ctx)
	defer func() { _ = fs.Close(context.Background()) }()

	// Warm the remote list once so subsequent iterations hit the commit
	// path (cache miss -> LoadRemoteChildren -> commitRemoteList).
	if _, err := fs.List(ctx, "/"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Force a fresh remote fetch each round by clearing the VFS-side
		// list cache (the TTL would otherwise serve the cache and never
		// reach CommitList).
		for i := 0; i < 100; i++ {
			_, _ = fs.List(ctx, "/")
			fs.view.mu.Lock()
			clear(fs.view.lists)
			fs.view.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = fs.SetModTime(ctx, "/a.txt", time.Unix(int64(i), 0))
		}
	}()
	wg.Wait()
}
