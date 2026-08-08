package read

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/yinzhenyu/qrypt/internal/vfs/vfstypes"
)

// recordingHealth records (op, err) pairs from the read domain.
type recordingHealth struct {
	mu  sync.Mutex
	ops []struct {
		op  string
		err error
	}
}

func (h *recordingHealth) RecordResult(op string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ops = append(h.ops, struct {
		op  string
		err error
	}{op: op, err: err})
}

func (h *recordingHealth) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.ops)
}

func (h *recordingHealth) lastErr() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.ops) == 0 {
		return nil
	}
	return h.ops[len(h.ops)-1].err
}

// TestNilHealthRecorderWorks: a nil HealthRecorder must not change read
// behavior - the reader falls back to a no-op sink.
func TestNilHealthRecorderWorks(t *testing.T) {
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
	rc.Close()
	if string(buf) != "hello" {
		t.Fatalf("read = %q, want %q", buf, "hello")
	}
}

// TestHealthRecordsSuccess: a successful read records one health result
// with a nil error.
func TestHealthRecordsSuccess(t *testing.T) {
	health := &recordingHealth{}
	r := NewReader(ReaderDeps{
		Host:   stubHostWithData{stubHost: stubHost{}, data: "0123456789"},
		State:  NewState(nil),
		Health: health,
	})
	rc, err := r.Read(context.Background(), "/f.txt", 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadFull(rc, make([]byte, 4))
	rc.Close()
	if n := health.count(); n != 1 {
		t.Fatalf("health records = %d, want 1", n)
	}
	if err := health.lastErr(); err != nil {
		t.Fatalf("successful read recorded error: %v", err)
	}
}

// TestHealthRecordsResolveError: a resolve failure records one health
// result carrying the error.
func TestHealthRecordsResolveError(t *testing.T) {
	health := &recordingHealth{}
	host := stubHost{resolveErr: errors.New("not found")}
	r := NewReader(ReaderDeps{
		Host:   host,
		State:  NewState(nil),
		Health: health,
	})
	if _, err := r.Read(context.Background(), "/missing.txt", 0, 4); err == nil {
		t.Fatal("want resolve error")
	}
	if n := health.count(); n != 1 {
		t.Fatalf("health records = %d, want 1", n)
	}
	if err := health.lastErr(); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("resolve failure recorded err = %v, want the resolve error", err)
	}
}

// TestHealthRecordsStagingError: a staging flush failure records one
// health result carrying the error.
func TestHealthRecordsStagingError(t *testing.T) {
	health := &recordingHealth{}
	host := &failingStagingHost{}
	r := NewReader(ReaderDeps{
		Host:   host,
		State:  NewState(nil),
		Health: health,
	})
	if _, err := r.Read(context.Background(), "/p.txt", 0, 4); err == nil {
		t.Fatal("want staging error")
	}
	if n := health.count(); n != 1 {
		t.Fatalf("health records = %d, want 1", n)
	}
	if err := health.lastErr(); err == nil || !strings.Contains(err.Error(), "staging boom") {
		t.Fatalf("staging failure recorded err = %v, want the staging error", err)
	}
}

// TestHealthReadStreamRecordsOpen: ReadStream records exactly one health
// result - the outcome of OPENING the stream, not errors surfaced later
// while consuming it. This locks the current metric definition.
func TestHealthReadStreamRecordsOpen(t *testing.T) {
	health := &recordingHealth{}
	host := stubHostWithData{stubHost: stubHost{}, data: strings.Repeat("x", 3*ChunkSize+10)}
	r := NewReader(ReaderDeps{
		Host:   host,
		State:  NewState(nil),
		Health: health,
	})
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
	if n := health.count(); n != 1 {
		t.Fatalf("stream health records = %d, want exactly 1 (open outcome)", n)
	}
	if err := health.lastErr(); err != nil {
		t.Fatalf("successful stream open recorded error: %v", err)
	}
}

// failingStagingHost reports a pending upload whose staging flush fails.
type failingStagingHost struct {
	stubHost
}

func (h *failingStagingHost) PendingUpload(string) (vfstypes.PendingUpload, bool, error) {
	return vfstypes.PendingUpload{FID: "pending-id", Path: "/p.txt", LocalPath: "/nonexistent/staging.bin"}, true, nil
}

func (h *failingStagingHost) FlushStaging(string) error {
	return errors.New("staging boom")
}
