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

	"github.com/yinzhenyu/qrypt/internal/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// recordingObserver records observer calls so tests can assert the
// read domain drives the debug sink on every path.
type recordingObserver struct {
	mu       sync.Mutex
	begins   []vfstypes.DebugActiveOp
	updates  []uint64
	finishes []uint64
	reads    int
	nextOpID int
}

func (o *recordingObserver) DebugNextOpID() string {
	return "obs-op"
}
func (o *recordingObserver) DebugBeginActive(op vfstypes.DebugActiveOp) uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.begins = append(o.begins, op)
	return uint64(len(o.begins))
}
func (o *recordingObserver) DebugUpdateActive(id uint64, fn func(*vfstypes.DebugActiveOp)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.updates = append(o.updates, id)
}
func (o *recordingObserver) DebugFinishActive(id uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finishes = append(o.finishes, id)
}
func (o *recordingObserver) DebugRecordRead(opID, path, remoteID string, offset, requested, bytes int64, source string, cacheHits, cacheMisses, chunks int64, started time.Time, extra map[string]any, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reads++
}
func (o *recordingObserver) DebugRecordReadDetail(ctx context.Context, path, remoteID, phase string, offset, requested, bytes int64, started time.Time, extra map[string]any, err error) {
}
func (o *recordingObserver) DebugCacheCounters() (hits, misses int64) { return 0, 0 }

func (o *recordingObserver) counts() (begins, finishes, reads int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.begins), len(o.finishes), o.reads
}

// stagingHost serves a pending staging read: PendingUpload reports a
// pending record and FlushStaging materializes the staging file.
type stagingHost struct {
	stubHost
	path string
	data []byte
}

func (h *stagingHost) PendingUpload(string) (vfstypes.PendingUpload, bool, error) {
	return vfstypes.PendingUpload{FID: "pending-id", Path: "/p.txt", LocalPath: h.path}, true, nil
}

func (h *stagingHost) FlushStaging(localPath string) error {
	return os.WriteFile(localPath, h.data, 0o644)
}

// stubHostWithData serves reads from an in-memory blob (like stubHost but
// with real content so the reader has a success path).
type stubHostWithData struct {
	stubHost
	data string
}

func (h stubHostWithData) Resolve(ctx context.Context, path string) (drive.Entry, error) {
	return drive.Entry{ID: "remote-id", Name: path, Size: int64(len(h.data))}, nil
}

func (h stubHostWithData) DriverRead(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset >= int64(len(h.data)) {
		return io.NopCloser(strings.NewReader("")), nil
	}
	end := offset + size
	if end > int64(len(h.data)) {
		end = int64(len(h.data))
	}
	return io.NopCloser(strings.NewReader(h.data[offset:end])), nil
}

// TestReaderNilObserverWorks: a nil observer must not change read behavior -
// the reader falls back to a no-op sink.
func TestReaderNilObserverWorks(t *testing.T) {
	r := NewReader(stubHostWithData{stubHost: stubHost{}, data: "hello world"}, NewState(nil), nil)
	rc, err := r.Read(context.Background(), "/f.txt", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if string(buf) != "hello" {
		t.Fatalf("read = %q, want %q", buf, "hello")
	}
}

// TestObserverReceivesLifecycle: a successful remote read begins active
// operations (one for the read, one per loaded chunk window) and finishes
// every one of them. A double Close must not re-finish anything.
func TestObserverReceivesLifecycle(t *testing.T) {
	obs := &recordingObserver{}
	r := NewReader(stubHostWithData{stubHost: stubHost{}, data: "0123456789"}, NewState(nil), obs)
	rc, err := r.Read(context.Background(), "/f.txt", 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	begins, finishes, reads := obs.counts()
	if begins == 0 || begins != finishes {
		t.Fatalf("lifecycle counts: begins=%d finishes=%d, want paired and >=1", begins, finishes)
	}
	if reads != 1 {
		t.Fatalf("read records = %d, want 1", reads)
	}
	// The second Close must be a no-op for the observer.
	b2, f2, r2 := obs.counts()
	if b2 != begins || f2 != finishes || r2 != reads {
		t.Fatalf("double Close changed observer counts: (%d,%d,%d) -> (%d,%d,%d)", begins, finishes, reads, b2, f2, r2)
	}
}

// TestObserverStagingPath: a pending staging read also begins and finishes
// exactly one active operation.
func TestObserverStagingPath(t *testing.T) {
	obs := &recordingObserver{}
	host := &stagingHost{data: []byte("payload")}
	host.path = filepath.Join(t.TempDir(), "staging.bin")
	r := NewReader(host, NewState(nil), obs)
	rc, err := r.Read(context.Background(), "/p.txt", 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	begins, finishes, _ := obs.counts()
	if begins != 1 || finishes != 1 {
		t.Fatalf("staging lifecycle: begins=%d finishes=%d, want 1/1", begins, finishes)
	}
}

// TestObserverErrorPathFinishes: when resolve fails, the active operation
// is finished (and the read recorded) even though no closer exists.
func TestObserverErrorPathFinishes(t *testing.T) {
	obs := &recordingObserver{}
	host := stubHost{resolveErr: errors.New("not found")}
	r := NewReader(host, NewState(nil), obs)
	if _, err := r.Read(context.Background(), "/missing.txt", 0, 4); err == nil {
		t.Fatal("want resolve error")
	}
	begins, finishes, reads := obs.counts()
	if begins != 1 || finishes != 1 {
		t.Fatalf("error lifecycle: begins=%d finishes=%d, want 1/1", begins, finishes)
	}
	if reads != 1 {
		t.Fatalf("error read records = %d, want 1", reads)
	}
}

// TestHostWithoutObserverStillObservable: the host surface and the observer
// are independent - a host that does NOT implement ReadObserver can still be
// paired with an explicit observer, proving the injection is constructive,
// not inferred from the host type.
func TestHostWithoutObserverStillObservable(t *testing.T) {
	obs := &recordingObserver{}
	host := stubHostWithData{stubHost: stubHost{}, data: "abc"}
	r := NewReader(host, NewState(nil), obs)
	rc, err := r.Read(context.Background(), "/f.txt", 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	begins, finishes, _ := obs.counts()
	if begins == 0 || begins != finishes {
		t.Fatalf("explicit observer counts: begins=%d finishes=%d, want paired and >=1", begins, finishes)
	}
}

// TestObserverStreamPath: ReadStream also completes its active operation on
// close, exactly once.
func TestObserverStreamPath(t *testing.T) {
	obs := &recordingObserver{}
	host := stubHostWithData{stubHost: stubHost{}, data: strings.Repeat("x", 3*ChunkSize+10)}
	r := NewReader(host, NewState(nil), obs)
	rc, err := r.ReadStream(context.Background(), "/big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	begins, finishes, reads := obs.counts()
	if begins == 0 || begins != finishes {
		t.Fatalf("stream lifecycle: begins=%d finishes=%d, want paired and >=1", begins, finishes)
	}
	if reads != 1 {
		t.Fatalf("stream read records = %d, want 1", reads)
	}
	// Double Close is a no-op for the observer.
	b2, f2, r2 := obs.counts()
	if b2 != begins || f2 != finishes || r2 != reads {
		t.Fatalf("double Close changed stream counts: (%d,%d,%d) -> (%d,%d,%d)", begins, finishes, reads, b2, f2, r2)
	}
}
