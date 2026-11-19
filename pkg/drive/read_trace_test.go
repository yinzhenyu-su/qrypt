package drive

import (
	"testing"
	"time"
)

// A coarse platform timer can report the first body block within the same
// clock tick as the range open, so every timing is a real zero. Snapshot must
// still publish those keys: the window did deliver data, and a consumer asking
// "has the first byte arrived" cannot distinguish a missing key from "no body
// progress yet".
func TestTraceKeepsObservedZeroFirstTimings(t *testing.T) {
	trace := &ReadTrace{}
	trace.stats.firstBodyObserved = true
	trace.stats.firstChunkObserved = true

	snapshot := trace.Snapshot()
	for _, key := range []string{"first_body_us", "first_chunk_us"} {
		value, ok := snapshot[key].(int64)
		if !ok {
			t.Fatalf("snapshot dropped observed timing %q: %v", key, snapshot)
		}
		if value != 0 {
			t.Fatalf("snapshot[%q] = %d, want the observed zero", key, value)
		}
	}
}

func TestTraceFirstTimingsRecordOnce(t *testing.T) {
	trace := &ReadTrace{}
	started := time.Now().Add(-time.Hour)
	trace.bodyProgress(10, 0, 10, started)
	firstBody := trace.Snapshot()["first_body_us"].(int64)
	if firstBody < int64(time.Hour/time.Microsecond) {
		t.Fatalf("first_body_us = %d, want the first body progress", firstBody)
	}

	trace.bodyProgress(ReadChunkSize-10, 10, ReadChunkSize, started)
	firstChunk := trace.Snapshot()["first_chunk_us"].(int64)
	if firstChunk < firstBody {
		t.Fatalf("first_chunk_us = %d, want at least first_body_us = %d", firstChunk, firstBody)
	}

	// Later progress must not overwrite the first-arrival timings.
	trace.bodyProgress(10, ReadChunkSize, ReadChunkSize+10, time.Now())
	snapshot := trace.Snapshot()
	if got := snapshot["first_body_us"].(int64); got != firstBody {
		t.Fatalf("first_body_us = %d after later progress, want %d", got, firstBody)
	}
	if got := snapshot["first_chunk_us"].(int64); got != firstChunk {
		t.Fatalf("first_chunk_us = %d after later progress, want %d", got, firstChunk)
	}
}

func TestTraceOmitsUnobservedFirstTimings(t *testing.T) {
	snapshot := (&ReadTrace{}).Snapshot()
	for _, key := range []string{"first_body_us", "first_chunk_us"} {
		if _, ok := snapshot[key]; ok {
			t.Fatalf("snapshot invented %q without body progress: %v", key, snapshot)
		}
	}
}
