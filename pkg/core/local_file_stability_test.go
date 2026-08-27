package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitLocalFileStableReturnsStableSnapshot(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "stable.txt")
	if err := os.WriteFile(localPath, []byte("stable"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Core{}

	got, err := c.WaitLocalFileStable(context.Background(), localPath, LocalFileStabilityOptions{
		QuietMS: 10,
		PollMS:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stable || got.Path != localPath || got.Size != int64(len("stable")) || got.QuietMS != 10 {
		t.Fatalf("stability = %+v, want stable snapshot", got)
	}
}

func TestWaitLocalFileStableWaitsAfterChange(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "changing.txt")
	if err := os.WriteFile(localPath, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The quiet window must comfortably outlast the writer goroutine's
	// scheduling delay: on loaded CI runners the 8ms sleep can stretch well
	// past a 20ms window, and the waiter would legitimately report the
	// pre-change snapshot as stable.
	opts := LocalFileStabilityOptions{QuietMS: 250, PollMS: 2}
	c := &Core{}
	go func() {
		time.Sleep(8 * time.Millisecond)
		_ = os.WriteFile(localPath, []byte("changed"), 0o644)
	}()

	start := time.Now()
	got, err := c.WaitLocalFileStable(context.Background(), localPath, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Size != int64(len("changed")) {
		t.Fatalf("size = %d, want changed size", got.Size)
	}
	if time.Since(start) < time.Duration(opts.QuietMS)*time.Millisecond {
		t.Fatalf("returned before quiet window elapsed")
	}
}

func TestWaitLocalFileStableRejectsDirectory(t *testing.T) {
	c := &Core{}
	_, err := c.WaitLocalFileStable(context.Background(), t.TempDir(), LocalFileStabilityOptions{QuietMS: 1, PollMS: 1})
	if err == nil {
		t.Fatal("WaitLocalFileStable directory err = nil, want error")
	}
}
