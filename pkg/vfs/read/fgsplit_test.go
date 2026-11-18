package read

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type fgSplitRecordingDriver struct {
	*drive.FakeDriver
	mu    sync.Mutex
	calls [][2]int64
	caps  []drive.Capability
}

func (d *fgSplitRecordingDriver) Capabilities() []drive.Capability { return d.caps }

func (d *fgSplitRecordingDriver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	d.mu.Lock()
	d.calls = append(d.calls, [2]int64{offset, size})
	d.mu.Unlock()
	return d.FakeDriver.Read(ctx, entry, offset, size)
}

func (d *fgSplitRecordingDriver) snapshot() [][2]int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([][2]int64, len(d.calls))
	copy(out, d.calls)
	return out
}

func newFGSplitTestReader(t *testing.T, content []byte) (*Reader, *fgSplitRecordingDriver) {
	t.Helper()
	base := drive.NewFakeDriver()
	if err := base.Seed(map[string]string{"file.bin": string(content)}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	drv := &fgSplitRecordingDriver{FakeDriver: base, caps: []drive.Capability{drive.CapabilityParallelRange}}
	entry := drive.Entry{
		ID:      "0/file.bin",
		Name:    "file.bin",
		Size:    int64(len(content)),
		ModTime: time.Unix(1700000000, 0),
	}
	source, err := drive.NewReadSource(drv, nil)
	if err != nil {
		t.Fatalf("NewReadSource: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })
	reader := newTestReader(testReaderDeps{
		Host:   &policyHost{driver: drv, entry: entry, cacheKey: "cache"},
		State:  NewState(nil),
		Source: source,
	})
	return reader, drv
}

func readChunkAt(t *testing.T, reader *Reader, chunk int64) []byte {
	t.Helper()
	ctx := WithoutReadPrefetch(WithReadIntent(context.Background(), ReadIntentInteractive))
	rc, err := reader.Read(ctx, "/file.bin", chunk*ChunkSize, ChunkSize)
	if err != nil {
		t.Fatalf("Read chunk %d: %v", chunk, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll chunk %d: %v", chunk, err)
	}
	return data
}

func callsInWindow(calls [][2]int64, firstChunk int64, chunks int64) int {
	start, end := firstChunk*ChunkSize, (firstChunk+chunks)*ChunkSize
	n := 0
	for _, call := range calls {
		if call[0] >= start && call[0] < end {
			n++
		}
	}
	return n
}

// waitWindowIdle waits for the async window fetch to finish so the test's
// threshold restoration in cleanup cannot race with a running fetch.
func waitWindowIdle(t *testing.T, reader *Reader) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reader.state.windows.mu.Lock()
		active := len(reader.state.windows.loads)
		reader.state.windows.mu.Unlock()
		if active == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("window fetch did not finish")
}

func setSlowStreak(t *testing.T, reader *Reader, windows int) {
	t.Helper()
	reader.fgSlowWindows = windows
}

func setSplitThresholds(t *testing.T, reader *Reader, enter, exit int64) {
	t.Helper()
	reader.fgEnterBytesPerSec, reader.fgExitBytesPerSec = enter, exit
}

// TestAdaptiveForegroundSplit pins the adaptive switch: a slow foreground
// window flips the object into the split mode for the following windows, a
// fast one keeps a single stream, and QRYPT_FG_SPLIT=0 disables the feature
// for a run (it is on by default).
func TestAdaptiveForegroundSplit(t *testing.T) {
	content := make([]byte, 4<<20)
	for i := range content {
		content[i] = byte(i)
	}

	t.Run("slow window switches the next window to split", func(t *testing.T) {
		t.Setenv("QRYPT_FG_SPLIT", "1")
		reader, drv := newFGSplitTestReader(t, content)
		setSlowStreak(t, reader, 1)                 // one slow sample is enough in the test
		setSplitThresholds(t, reader, 1<<40, 1<<40) // any measured rate counts as slow
		first := readChunkAt(t, reader, 0)
		if len(first) != int(ChunkSize) {
			t.Fatalf("first chunk = %d bytes, want %d", len(first), ChunkSize)
		}
		if n := callsInWindow(drv.snapshot(), 0, 1); n != 1 {
			t.Fatalf("first window calls = %d, want 1 (split arms only after a measured slow window)", n)
		}
		second := readChunkAt(t, reader, 1)
		if len(second) != int(ChunkSize) {
			t.Fatalf("second chunk = %d bytes, want %d", len(second), ChunkSize)
		}
		for i := range second {
			if second[i] != content[int(ChunkSize)+i] {
				t.Fatalf("second chunk byte %d mismatch", i)
			}
		}
		if n := callsInWindow(drv.snapshot(), 1, 1); n < 2 {
			t.Fatalf("second window calls = %d, want the window split into several ranges", n)
		}
	})

	t.Run("fast window keeps one stream", func(t *testing.T) {
		t.Setenv("QRYPT_FG_SPLIT", "1")
		reader, drv := newFGSplitTestReader(t, content)
		setSplitThresholds(t, reader, 0, 0) // rate < 0 never holds: never enter split mode
		readChunkAt(t, reader, 0)
		readChunkAt(t, reader, 1)
		calls := drv.snapshot()
		if n := callsInWindow(calls, 0, 1); n != 1 {
			t.Fatalf("first window calls = %d, want 1", n)
		}
		if n := callsInWindow(calls, 1, 1); n != 1 {
			t.Fatalf("second window calls = %d, want 1 for a fast provider", n)
		}
	})

	t.Run("one slow window arms the next one", func(t *testing.T) {
		t.Setenv("QRYPT_FG_SPLIT", "1")
		reader, drv := newFGSplitTestReader(t, content)
		setSplitThresholds(t, reader, 1<<40, 1<<40)
		readChunkAt(t, reader, 0) // no sample yet: single stream
		calls := drv.snapshot()
		if n := callsInWindow(calls, 0, 1); n != 1 {
			t.Fatalf("first window calls = %d, want 1", n)
		}
		// The first slow sample arms the split, so the next window is fetched
		// over several ranges instead of stalling playback for a whole window.
		readChunkAt(t, reader, 1)
		if n := callsInWindow(drv.snapshot(), 1, 1); n < 2 {
			t.Fatalf("second window calls = %d, want the window split after one slow sample", n)
		}
	})

	t.Run("split windows use an even part layout", func(t *testing.T) {
		t.Setenv("QRYPT_FG_SPLIT", "1")
		reader, drv := newFGSplitTestReader(t, content)
		setSplitThresholds(t, reader, 1<<40, 1<<40)
		readChunkAt(t, reader, 0) // measure a slow window and arm the split
		ctx := WithoutReadPrefetch(WithReadIntent(context.Background(), ReadIntentInteractive))
		rc, err := reader.Read(ctx, "/file.bin", ChunkSize, int64(len(content))-ChunkSize)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if _, err := io.ReadAll(rc); err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		_ = rc.Close()
		// A read larger than one chunk opens the wide window, clamped to the
		// file end here; the split must divide that window evenly.
		windowBytes := int64(len(content)) - ChunkSize
		wantParts := foregroundSplitStreams
		want := (windowBytes + int64(wantParts) - 1) / int64(wantParts)
		var parts []int64
		for _, call := range drv.snapshot() {
			if call[0] >= ChunkSize {
				parts = append(parts, call[1])
			}
		}
		if len(parts) != wantParts {
			t.Fatalf("split parts = %v, want %d evenly sized ranges", parts, wantParts)
		}
		var total int64
		for _, size := range parts {
			if size != want {
				t.Fatalf("split part sizes = %v, want %d each", parts, want)
			}
			total += size
		}
		if total != windowBytes {
			t.Fatalf("split parts cover %d bytes, want %d", total, windowBytes)
		}
		waitWindowIdle(t, reader)
	})

	t.Run("QRYPT_FG_SPLIT=0 keeps one stream", func(t *testing.T) {
		t.Setenv("QRYPT_FG_SPLIT", "0")
		reader, drv := newFGSplitTestReader(t, content)
		setSplitThresholds(t, reader, 1<<40, 1<<40)
		readChunkAt(t, reader, 0)
		readChunkAt(t, reader, 1)
		readChunkAt(t, reader, 2)
		if n := callsInWindow(drv.snapshot(), 1, 1); n != 1 {
			t.Fatalf("split ran while QRYPT_FG_SPLIT=0: calls = %d", n)
		}
	})
}

func setThrottleMemory(t *testing.T, reader *Reader, d time.Duration) {
	t.Helper()
	reader.fgThrottleMemory = d
}

// TestAdaptiveForegroundSplitThrottleMemory pins the cross-object hedge: after
// any object measured a slow window, the next object that has no sample yet
// starts split instead of paying a cold-start single-stream window.
func TestAdaptiveForegroundSplitThrottleMemory(t *testing.T) {
	t.Setenv("QRYPT_FG_SPLIT", "1")
	content := make([]byte, 4<<20)
	reader, _ := newFGSplitTestReader(t, content)
	setSplitThresholds(t, reader, 1<<40, 1<<40) // any measured rate counts as slow
	setSlowStreak(t, reader, 1)
	setThrottleMemory(t, reader, time.Minute)
	if got := reader.foregroundSplitRequested("other.bin"); got != 0 {
		t.Fatalf("unknown object split before any slow sample: %d", got)
	}
	readChunkAt(t, reader, 0) // one slow sample on this object
	if got := reader.foregroundSplitRequested("other.bin"); got != foregroundSplitStreams {
		t.Fatalf("new object split = %d, want %d after a recent slow sample", got, foregroundSplitStreams)
	}
	// An object that already has a fast sample is not hedged.
	reader.fgMu.Lock()
	reader.fgSplit["fast.bin"] = &fgSplitState{rate: 5 << 20}
	reader.fgMu.Unlock()
	if got := reader.foregroundSplitRequested("fast.bin"); got != 0 {
		t.Fatalf("fast object split = %d, want 0", got)
	}
	// The memory expires: an old slow timestamp no longer hedges new objects.
	reader.fgMu.Lock()
	reader.fgLastSlow = time.Now().Add(-2 * time.Minute)
	reader.fgMu.Unlock()
	if got := reader.foregroundSplitRequested("later.bin"); got != 0 {
		t.Fatalf("expired memory still split: %d", got)
	}
}
