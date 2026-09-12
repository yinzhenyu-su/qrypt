package read

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// recordingCounters captures every sample so tests can assert the read domain
// records exactly one per Read call, on every path.
type recordingCounters struct {
	mu      sync.Mutex
	samples []counterSample
}

type counterSample struct {
	bytes   int64
	elapsed time.Duration
	err     error
}

func (c *recordingCounters) RecordRead(bytes int64, elapsed time.Duration, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, counterSample{bytes: bytes, elapsed: elapsed, err: err})
}

func (c *recordingCounters) recorded() []counterSample {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]counterSample, len(c.samples))
	copy(out, c.samples)
	return out
}

// TestCountersRecordOneSamplePerRead: a read that walks several chunks and
// windows must produce exactly one sample, not one per internal step.
func TestCountersRecordOneSamplePerRead(t *testing.T) {
	data := strings.Repeat("a", 2*ChunkSize)
	counters := &recordingCounters{}
	r := NewReader(ReaderDeps{
		Host:     stubHostWithData{stubHost: stubHost{}, data: data},
		State:    NewState(nil),
		Counters: counters,
	})

	rc, err := r.Read(context.Background(), "/big.bin", 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	samples := counters.recorded()
	if len(samples) != 1 {
		t.Fatalf("counter samples = %d, want 1", len(samples))
	}
	if samples[0].bytes != int64(len(payload)) {
		t.Fatalf("sample bytes = %d, want %d", samples[0].bytes, len(payload))
	}
	if samples[0].err != nil {
		t.Fatalf("successful read recorded error: %v", samples[0].err)
	}
}

// TestCountersRecordResolveFailure: a failed attempt is still one sample,
// carrying the error and no bytes.
func TestCountersRecordResolveFailure(t *testing.T) {
	counters := &recordingCounters{}
	r := NewReader(ReaderDeps{
		Host:     stubHost{resolveErr: errors.New("not found")},
		State:    NewState(nil),
		Counters: counters,
	})

	if _, err := r.Read(context.Background(), "/missing.txt", 0, 4); err == nil {
		t.Fatal("want resolve error")
	}
	samples := counters.recorded()
	if len(samples) != 1 {
		t.Fatalf("counter samples = %d, want 1", len(samples))
	}
	if samples[0].err == nil {
		t.Fatal("failed read recorded no error")
	}
	if samples[0].bytes != 0 {
		t.Fatalf("failed read recorded %d bytes, want 0", samples[0].bytes)
	}
}

// TestCountersRecordStagingReadWithZeroBytes: the staging passthrough only
// opens a local file, so it counts an operation with no materialized bytes.
func TestCountersRecordStagingReadWithZeroBytes(t *testing.T) {
	local := filepath.Join(t.TempDir(), "pending.staging")
	if err := os.WriteFile(local, []byte("staged payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	counters := &recordingCounters{}
	r := NewReader(ReaderDeps{
		Host:     stagingFileHost{localPath: local},
		State:    NewState(nil),
		Counters: counters,
	})

	rc, err := r.Read(context.Background(), "/p.txt", 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()

	samples := counters.recorded()
	if len(samples) != 1 {
		t.Fatalf("counter samples = %d, want 1", len(samples))
	}
	if samples[0].err != nil {
		t.Fatalf("staging read recorded error: %v", samples[0].err)
	}
	if samples[0].bytes != 0 {
		t.Fatalf("staging read recorded %d bytes, want 0", samples[0].bytes)
	}
}

// TestNilCounterRecorderWorks: a nil CounterRecorder must not change read
// behavior - the reader falls back to a no-op sink.
func TestNilCounterRecorderWorks(t *testing.T) {
	r := NewReader(ReaderDeps{
		Host:  stubHostWithData{stubHost: stubHost{}, data: "hello world"},
		State: NewState(nil),
	})
	rc, err := r.Read(context.Background(), "/f.txt", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if string(buf) != "hello" {
		t.Fatalf("read = %q, want %q", buf, "hello")
	}
}

// stagingFileHost reports a pending upload backed by a real local file.
type stagingFileHost struct {
	stubHost
	localPath string
}

func (h stagingFileHost) PendingUpload(string) (vfstypes.PendingUpload, bool, error) {
	return vfstypes.PendingUpload{FID: "pending-id", Path: "/p.txt", LocalPath: h.localPath}, true, nil
}
