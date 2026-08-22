package vfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// A generation replaced after its flush never reaches a worker (its debounce
// timer is superseded by the newer write), so no worker pass cleans its
// staging file. Start must run the live sweeper that removes it.
func TestStartSweepsUnreferencedStaging(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close(context.Background()) }()
	fs.stagingSweepInterval = 5 * time.Millisecond
	fs.Start(ctx)

	orphan := filepath.Join(fs.uploads.Store().StagingDir(), "leaked-generation.staging")
	if err := os.WriteFile(orphan, []byte("superseded generation"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(orphan); os.IsNotExist(err) {
			return
		} else if err != nil && time.Now().After(deadline) {
			t.Fatalf("stat orphan: %v", err)
		} else if err == nil && time.Now().After(deadline) {
			t.Fatal("leaked staging file was not swept after Start")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
