package listing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// recordingHealth records (op, err) pairs from the listing domain.
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

// TestNilHealthRecorderWorks: a nil HealthRecorder must not change listing
// behavior - the lister falls back to a no-op sink.
func TestNilHealthRecorderWorks(t *testing.T) {
	host := &fakeRuntimeHost{children: []drive.Entry{{ID: "c1", Name: "a.txt"}}}
	l := NewLister(ListerDeps{Remote: host, View: host, State: NewState()})
	entries, err := l.List(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("entries = %+v, want [a.txt]", entries)
	}
}

// TestHealthRecordsSuccess: a successful List records exactly one health
// result with a nil error.
func TestHealthRecordsSuccess(t *testing.T) {
	health := &recordingHealth{}
	host := &fakeRuntimeHost{children: []drive.Entry{{ID: "c1", Name: "a.txt"}}}
	l := NewLister(ListerDeps{Remote: host, View: host, State: NewState(), Health: health})
	if _, err := l.List(context.Background(), "/"); err != nil {
		t.Fatal(err)
	}
	if n := health.count(); n != 1 {
		t.Fatalf("health records = %d, want 1", n)
	}
	if err := health.lastErr(); err != nil {
		t.Fatalf("successful list recorded error: %v", err)
	}
}

// TestHealthRecordsError: a backend failure records exactly one health
// result carrying the error.
func TestHealthRecordsError(t *testing.T) {
	health := &recordingHealth{}
	host := &fakeRuntimeHost{childErr: errors.New("backend boom")}
	l := NewLister(ListerDeps{Remote: host, View: host, State: NewState(), Health: health})
	if _, err := l.List(context.Background(), "/"); err == nil {
		t.Fatal("want backend error")
	}
	if n := health.count(); n != 1 {
		t.Fatalf("health records = %d, want 1", n)
	}
	if err := health.lastErr(); err == nil || err.Error() != "backend boom" {
		t.Fatalf("failure recorded err = %v, want the backend error", err)
	}
}

// TestHealthRecordsCacheHit: a List served from the fresh view cache still
// records exactly one health result (the metric is "list completed", not
// "remote IO happened").
func TestHealthRecordsCacheHit(t *testing.T) {
	health := &recordingHealth{}
	host := &fakeRuntimeHost{children: []drive.Entry{{ID: "c1", Name: "a.txt"}}}
	host.cache = map[string]listCacheEntry{
		"/": {entries: []drive.Entry{{ID: "c1", Name: "a.txt"}}, expires: time.Now().Add(time.Minute)},
	}
	l := NewLister(ListerDeps{Remote: host, View: host, State: NewState(), Health: health})
	if _, err := l.List(context.Background(), "/"); err != nil {
		t.Fatal(err)
	}
	if n := health.count(); n != 1 {
		t.Fatalf("cache-hit health records = %d, want 1", n)
	}
}

// TestHealthPrefetchDoesNotDoubleRecord: background directory prefetch does
// not record additional health results - List reports exactly one per call.
func TestHealthPrefetchDoesNotDoubleRecord(t *testing.T) {
	health := &recordingHealth{}
	host := &fakeRuntimeHost{children: []drive.Entry{{ID: "c1", Name: "a.txt"}}}
	l := NewLister(ListerDeps{Remote: host, View: host, State: NewState(), Health: health})
	if _, err := l.List(context.Background(), "/"); err != nil {
		t.Fatal(err)
	}
	// A second list is another health record, but never more than one per
	// list (prefetch of child dirs records nothing).
	if _, err := l.List(context.Background(), "/"); err != nil {
		t.Fatal(err)
	}
	if n := health.count(); n != 2 {
		t.Fatalf("health records = %d, want exactly 2 (one per List call)", n)
	}
}
