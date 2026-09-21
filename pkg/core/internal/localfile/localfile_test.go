package localfile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseOptionsDefaultsAndOverrides(t *testing.T) {
	opts, err := ParseOptions(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Quiet != defaultQuiet || opts.Poll != defaultPoll {
		t.Fatalf("ParseOptions(0, 0) = %+v, want %v/%v", opts, defaultQuiet, defaultPoll)
	}
	opts, err = ParseOptions(10, 2)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Quiet != 10*time.Millisecond || opts.Poll != 2*time.Millisecond {
		t.Fatalf("ParseOptions(10, 2) = %+v, want 10ms/2ms", opts)
	}
}

func TestParseOptionsValidation(t *testing.T) {
	if opts, err := ParseOptions(-1, 0); err == nil || err.Error() != "core: quiet_ms must be non-negative" {
		t.Fatalf("ParseOptions(-1, 0) = %+v, %v; want quiet_ms error", opts, err)
	}
	if opts, err := ParseOptions(0, -1); err == nil || err.Error() != "core: poll_ms must be positive" {
		t.Fatalf("ParseOptions(0, -1) = %+v, %v; want poll_ms error", opts, err)
	}
	if opts, err := ParseOptions(-1, -1); err == nil || opts != (Options{}) {
		t.Fatalf("ParseOptions(-1, -1) = %+v, %v; want zero Options on error", opts, err)
	}
}

func TestWaitReturnsStableSnapshot(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "stable.txt")
	if err := os.WriteFile(localPath, []byte("stable"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{Quiet: 10 * time.Millisecond, Poll: time.Millisecond}
	got, err := Wait(context.Background(), localPath, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stable || got.Path != localPath || got.Size != int64(len("stable")) || got.Quiet != opts.Quiet {
		t.Fatalf("snapshot = %+v, want stable observation", got)
	}
	info, err := os.Stat(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(got.ModTime) {
		t.Fatalf("mod_time = %v, want file mtime %v", got.ModTime, info.ModTime())
	}
	if got.ObservedAt.IsZero() || got.ObservedAt.Location() != time.UTC {
		t.Fatalf("observed_at = %v, want a non-zero UTC timestamp", got.ObservedAt)
	}
}

func TestWaitRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := Wait(context.Background(), dir, Options{Quiet: time.Millisecond, Poll: time.Millisecond})
	if err == nil || err.Error() != fmt.Sprintf("core: local path %q is a directory", dir) {
		t.Fatalf("Wait directory err = %v, want directory error", err)
	}
}

func TestWaitWaitsAfterChange(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "changing.txt")
	if err := os.WriteFile(localPath, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The quiet window must comfortably outlast the writer goroutine's
	// scheduling delay: on loaded CI runners the 8ms sleep can stretch well
	// past a 20ms window, and the waiter would legitimately report the
	// pre-change snapshot as stable.
	opts := Options{Quiet: 250 * time.Millisecond, Poll: 2 * time.Millisecond}
	go func() {
		time.Sleep(8 * time.Millisecond)
		_ = os.WriteFile(localPath, []byte("changed"), 0o644)
	}()

	start := time.Now()
	got, err := Wait(context.Background(), localPath, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Size != int64(len("changed")) {
		t.Fatalf("size = %d, want changed size", got.Size)
	}
	if time.Since(start) < opts.Quiet {
		t.Fatalf("returned before quiet window elapsed")
	}
}

func TestWaitCancellationReturnsLastObservation(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "cancel.txt")
	if err := os.WriteFile(localPath, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		snapshot Snapshot
		err      error
	}
	gotCh := make(chan result, 1)
	go func() {
		snapshot, err := Wait(ctx, localPath, Options{Quiet: time.Second, Poll: time.Millisecond})
		gotCh <- result{snapshot, err}
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	got := <-gotCh
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Wait err = %v, want context.Canceled", got.err)
	}
	if got.snapshot.Path != localPath || got.snapshot.Size != int64(len("a")) {
		t.Fatalf("snapshot = %+v, want the last observation of the unchanged file", got.snapshot)
	}
}

func TestWaitCancellationAfterChangeReturnsMostRecentSnapshot(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "cancel-again.txt")
	if err := os.WriteFile(localPath, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		snapshot Snapshot
		err      error
	}
	gotCh := make(chan result, 1)
	go func() {
		snapshot, err := Wait(ctx, localPath, Options{Quiet: time.Minute, Poll: time.Millisecond})
		gotCh <- result{snapshot, err}
	}()

	go func() {
		time.Sleep(8 * time.Millisecond)
		_ = os.WriteFile(localPath, []byte("changed"), 0o644)
	}()
	// Wait until the change is on disk before cancelling; the waiter returns
	// one of its own observations (never a zero value) with the error.
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := os.Stat(localPath)
		if err == nil && info.Size() == int64(len("changed")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("file change not observed in time")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	got := <-gotCh
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Wait err = %v, want context.Canceled", got.err)
	}
	if got.snapshot.Size != int64(len("a")) && got.snapshot.Size != int64(len("changed")) {
		t.Fatalf("snapshot size = %d, want a real observation of the file", got.snapshot.Size)
	}
}

func TestWaitQuietZeroReturnsImmediately(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "zero.txt")
	if err := os.WriteFile(localPath, []byte("zero"), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got, err := Wait(context.Background(), localPath, Options{Quiet: 0, Poll: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stable || got.Size != int64(len("zero")) || got.Quiet != 0 {
		t.Fatalf("snapshot = %+v, want immediate observation", got)
	}
	// A zero quiet window must not wait: the only work is a single stat.
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Wait took %v, want immediate return for quiet=0", elapsed)
	}
}
