package read

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/testsupport/simread"
)

// newSimSplitReader builds a reader over a localfs driver mediated by the
// simulated provider. The file holds exactly chunkCount chunks of deterministic
// content so reads map one-to-one onto 512 KiB chunks.
func newSimSplitReader(t *testing.T, chunkCount int, cfg simread.Config) (*Reader, *simread.Provider, []byte) {
	t.Helper()
	t.Setenv("QRYPT_FG_SPLIT", "")
	dir := t.TempDir()
	content := make([]byte, int64(chunkCount)*ChunkSize)
	for i := range content {
		content[i] = byte(i % 251)
	}
	path := filepath.Join(dir, "file.bin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	base := localfs.New(dir)
	if err := base.Init(t.Context()); err != nil {
		t.Fatalf("init localfs: %v", err)
	}
	sim := simread.Wrap(base, cfg)
	source, err := drive.NewReadSource(sim, nil)
	if err != nil {
		t.Fatalf("new read source: %v", err)
	}
	state := NewState(nil)
	entry := drive.Entry{ID: path, Name: "file.bin", Size: int64(len(content))}
	reader := newTestReader(testReaderDeps{
		Host:   &policyHost{driver: sim, entry: entry, cacheKey: "cache"},
		State:  state,
		Source: source,
	})
	t.Cleanup(func() {
		_ = state.Close()
		_ = source.Close()
	})
	return reader, sim, content
}

// assertRangeTiling requires the recorded ranges that overlap [start,end) to
// tile it exactly: ordered, contiguous, no gap and no overlap. A duplicated
// lookahead window or an overlapping split part shows up here as an overlap.
func assertRangeTiling(t *testing.T, requests []simread.Request, start, end int64) {
	t.Helper()
	var window []simread.Request
	for _, req := range requests {
		if req.Offset < end && req.Offset+req.Size > start {
			window = append(window, req)
		}
	}
	if len(window) == 0 {
		t.Fatalf("no ranges cover [%d,%d): %+v", start, end, requests)
	}
	sort.Slice(window, func(i, j int) bool { return window[i].Offset < window[j].Offset })
	cursor := start
	for _, req := range window {
		if req.Offset != cursor {
			t.Fatalf("ranges do not tile [%d,%d): next range starts at %d (cursor %d): %+v",
				start, end, req.Offset, cursor, window)
		}
		cursor += req.Size
	}
	if cursor != end {
		t.Fatalf("ranges cover [%d,%d), want [%d,%d): %+v", start, cursor, start, end, window)
	}
}

// TestSimAdaptiveForegroundSplitUnderBandwidth pins the default-on adaptive
// foreground split against a 150 KiB/s per-connection provider: one
// single-stream window measures below the enter threshold and arms the split,
// then the following windows are fetched over four parallel ranges, reaching
// well above the single-stream aggregate.
//
// The provider runs at TimeScale 2: the simulated 150 KiB/s is observed by the
// read path as ~300 KiB/s (still under the 350 KiB/s enter threshold), while
// the 512 KiB windows take ~0.43 real seconds instead of ~3.4. Both windows
// after the first measure the *sum* of their four part bodies, so the split
// rate stays at the per-connection rate and the split is not dropped again by
// the exit threshold.
func TestSimAdaptiveForegroundSplitUnderBandwidth(t *testing.T) {
	const chunkCount = 3
	reader, sim, content := newSimSplitReader(t, chunkCount, simread.Config{
		TimeScale:       2,
		RateBytesPerSec: 150 << 10,
	})

	start := time.Now()
	first := readChunkAt(t, reader, 0)
	single := time.Since(start)
	if !bytes.Equal(first, content[:ChunkSize]) {
		t.Fatalf("first chunk content mismatch")
	}
	if single < time.Second/2 {
		t.Fatalf("first chunk read in %s, expected an unthrottled single stream to be paced", single)
	}
	if n := len(sim.Requests()); n != 1 {
		t.Fatalf("first window opened %d ranges (%+v), want exactly one single-stream range", n, sim.Requests())
	}
	if req := sim.Requests()[0]; req.Size != ChunkSize {
		t.Fatalf("first window range = [%d,%d), want one %d-byte chunk", req.Offset, req.Offset+req.Size, ChunkSize)
	}

	start = time.Now()
	second := readChunkAt(t, reader, 1)
	third := readChunkAt(t, reader, 2)
	split := time.Since(start)
	if !bytes.Equal(second, content[ChunkSize:2*ChunkSize]) {
		t.Fatalf("second chunk content mismatch")
	}
	if !bytes.Equal(third, content[2*ChunkSize:]) {
		t.Fatalf("third chunk content mismatch")
	}

	requests := sim.Requests()
	assertRangeTiling(t, requests, ChunkSize, int64(chunkCount)*ChunkSize)
	parts := requests[1:]
	if len(parts) != 2*foregroundSplitStreams {
		t.Fatalf("split windows opened %d ranges, want %d (4 parts per chunk): %+v",
			len(parts), 2*foregroundSplitStreams, requests)
	}
	wantPart := ChunkSize / int64(foregroundSplitStreams)
	for _, part := range parts {
		if part.Size != wantPart {
			t.Fatalf("split part = [%d,%d), want %d-byte parts: %+v", part.Offset, part.Offset+part.Size, wantPart, parts)
		}
	}

	singleRate := float64(ChunkSize) / single.Seconds()
	splitRate := float64(2*ChunkSize) / split.Seconds()
	ratio := splitRate / singleRate
	if ratio < 1.5 {
		t.Fatalf("split aggregate = %.0f B/s vs single stream %.0f B/s (%.2fx); want >= 1.5x",
			splitRate, singleRate, ratio)
	}
	if stats := sim.Stats(); stats.MaxConcurrent < 2 {
		t.Fatalf("peak concurrent ranges = %d, want the split window's parts in flight together", stats.MaxConcurrent)
	}
	t.Logf("single-stream %.0f B/s, split aggregate %.0f B/s (%.2fx), peak concurrent %d",
		singleRate, splitRate, ratio, sim.Stats().MaxConcurrent)
}
