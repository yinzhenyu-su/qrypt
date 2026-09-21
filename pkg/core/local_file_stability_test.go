package core

import (
	"context"
	"encoding/json"
	"errors"
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

func TestWaitLocalFileStableValidationMessages(t *testing.T) {
	c := &Core{}
	_, err := c.WaitLocalFileStable(context.Background(), "/x", LocalFileStabilityOptions{QuietMS: -1})
	if err == nil || err.Error() != "core: quiet_ms must be non-negative" {
		t.Fatalf("negative quiet_ms err = %v, want quiet_ms message", err)
	}
	_, err = c.WaitLocalFileStable(context.Background(), "/x", LocalFileStabilityOptions{QuietMS: 1, PollMS: -1})
	if err == nil || err.Error() != "core: poll_ms must be positive" {
		t.Fatalf("negative poll_ms err = %v, want poll_ms message", err)
	}
	_, err = c.WaitLocalFileStable(context.Background(), "   ", LocalFileStabilityOptions{})
	if err == nil || err.Error() != "core: local path required" {
		t.Fatalf("blank path err = %v, want path-required message", err)
	}
}

func TestWaitLocalFileStableCancellation(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "cancel.txt")
	if err := os.WriteFile(localPath, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Core{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := c.WaitLocalFileStable(ctx, localPath, LocalFileStabilityOptions{QuietMS: 1000, PollMS: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if err.Error() != context.Canceled.Error() {
		t.Fatalf("err text = %q, want unreformatted context error", err.Error())
	}
}

func TestLocalFileStabilityOptionsJSONFields(t *testing.T) {
	raw, err := json.Marshal(LocalFileStabilityOptions{QuietMS: 5, PollMS: 2})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"quiet_ms":5,"poll_ms":2}` {
		t.Fatalf("options json = %s, want quiet_ms/poll_ms fields", raw)
	}
	raw, err = json.Marshal(LocalFileStabilityOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{}` {
		t.Fatalf("zero options json = %s, want empty object", raw)
	}
	var round LocalFileStabilityOptions
	if err := json.Unmarshal([]byte(`{"quiet_ms":10,"poll_ms":3}`), &round); err != nil {
		t.Fatal(err)
	}
	if round.QuietMS != 10 || round.PollMS != 3 {
		t.Fatalf("unmarshal = %+v, want quiet_ms=10 poll_ms=3", round)
	}
}

func TestLocalFileStabilityJSONFields(t *testing.T) {
	modTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	observedAt := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)
	raw, err := json.Marshal(LocalFileStability{
		Path:       "/p",
		Stable:     true,
		Size:       7,
		ModTime:    modTime,
		ObservedAt: observedAt,
		QuietMS:    250,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"path":"/p","stable":true,"size":7,"mod_time":"2026-01-02T03:04:05Z","observed_at":"2026-01-02T03:04:06Z","quiet_ms":250}`
	if string(raw) != want {
		t.Fatalf("stability json = %s, want %s", raw, want)
	}
	// Field names must survive a round-trip, not just marshal.
	var round LocalFileStability
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatal(err)
	}
	if round.Path != "/p" || !round.Stable || round.Size != 7 || !round.ModTime.Equal(modTime) || !round.ObservedAt.Equal(observedAt) || round.QuietMS != 250 {
		t.Fatalf("round-trip = %+v, want stable json fields", round)
	}
}
