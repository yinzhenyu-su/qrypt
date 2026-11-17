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

func readFirstChunk(t *testing.T, reader *Reader) []byte {
	t.Helper()
	ctx := WithoutReadPrefetch(WithReadIntent(context.Background(), ReadIntentInteractive))
	rc, err := reader.Read(ctx, "/file.bin", 0, ChunkSize)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return data
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

func setSplitThresholds(t *testing.T, enter, exit int64) {
	t.Helper()
	oldEnter, oldExit := fgSplitEnterBytesPerSec, fgSplitExitBytesPerSec
	fgSplitEnterBytesPerSec, fgSplitExitBytesPerSec = enter, exit
	t.Cleanup(func() {
		fgSplitEnterBytesPerSec, fgSplitExitBytesPerSec = oldEnter, oldExit
	})
}

func setSlowStreak(t *testing.T, windows int) {
	t.Helper()
	old := fgSplitSlowWindows
	fgSplitSlowWindows = windows
	t.Cleanup(func() { fgSplitSlowWindows = old })
}

// TestAdaptiveForegroundSplit pins the adaptive switch: a slow foreground
// window flips the object into the split mode for the following windows, a
// fast one keeps a single stream, and QRYPT_FG_SPLIT unset disables the
// feature entirely.
func TestAdaptiveForegroundSplit(t *testing.T) {
	content := make([]byte, 4<<20)
	for i := range content {
		content[i] = byte(i)
	}

	t.Run("slow window switches the next window to split", func(t *testing.T) {
		t.Setenv("QRYPT_FG_SPLIT", "1")
		setSlowStreak(t, 1)                 // one slow sample is enough in the test
		setSplitThresholds(t, 1<<40, 1<<40) // any measured rate counts as slow
		reader, drv := newFGSplitTestReader(t, content)
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
		setSplitThresholds(t, 0, 0) // rate < 0 never holds: never enter split mode
		reader, drv := newFGSplitTestReader(t, content)
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

	t.Run("one slow window is not enough", func(t *testing.T) {
		t.Setenv("QRYPT_FG_SPLIT", "1")
		setSplitThresholds(t, 1<<40, 1<<40)
		reader, drv := newFGSplitTestReader(t, content)
		readChunkAt(t, reader, 0) // no sample: single
		readChunkAt(t, reader, 1) // one slow sample: still single
		calls := drv.snapshot()
		if n := callsInWindow(calls, 0, 1); n != 1 {
			t.Fatalf("first window calls = %d, want 1", n)
		}
		if n := callsInWindow(calls, 1, 1); n != 1 {
			t.Fatalf("second window calls = %d, want 1 until the slow streak reaches the threshold", n)
		}
		readChunkAt(t, reader, 2) // second slow sample arms the split for the next window
		readChunkAt(t, reader, 3)
		if n := callsInWindow(drv.snapshot(), 3, 1); n < 2 {
			t.Fatalf("fourth window calls = %d, want the window split after two slow samples", n)
		}
	})

	t.Run("switch off keeps one stream", func(t *testing.T) {
		t.Setenv("QRYPT_FG_SPLIT", "")
		setSplitThresholds(t, 1<<40, 1<<40)
		reader, drv := newFGSplitTestReader(t, content)
		readChunkAt(t, reader, 0)
		readChunkAt(t, reader, 1)
		if n := callsInWindow(drv.snapshot(), 1, 1); n != 1 {
			t.Fatalf("split ran while QRYPT_FG_SPLIT was unset: calls = %d", n)
		}
	})
}
