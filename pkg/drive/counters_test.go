package drive

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCountersRecordRead(t *testing.T) {
	var c Counters

	c.RecordRead(1<<20, 30*time.Millisecond, nil)
	c.RecordRead(2<<20, 60*time.Millisecond, nil)
	c.RecordRead(0, 10*time.Millisecond, errors.New("read failed"))

	snapshot := c.Snapshot()
	if snapshot.Ops != 3 {
		t.Fatalf("ops = %d, want 3", snapshot.Ops)
	}
	if snapshot.Errors != 1 {
		t.Fatalf("errors = %d, want 1", snapshot.Errors)
	}
	if want := uint64(3 << 20); snapshot.Bytes != want {
		t.Fatalf("bytes = %d, want %d (failed reads must not count bytes)", snapshot.Bytes, want)
	}
	if snapshot.DurationMS != 100 {
		t.Fatalf("duration_ms = %d, want 100", snapshot.DurationMS)
	}
	if snapshot.MeanMS < 33.3 || snapshot.MeanMS > 33.4 {
		t.Fatalf("mean_ms = %v, want about 33.33", snapshot.MeanMS)
	}
	if got := snapshot.Latency.Samples(); got != 3 {
		t.Fatalf("latency samples = %d, want 3 (failures still have a duration)", got)
	}
}

func TestCountersFailedReadDoesNotCountBytes(t *testing.T) {
	var c Counters

	// A failed attempt may still report partial bytes; the byte total must
	// stay attributable to successes only.
	c.RecordRead(4096, 5*time.Millisecond, errors.New("boom"))

	if snapshot := c.Snapshot(); snapshot.Bytes != 0 {
		t.Fatalf("bytes = %d, want 0 after a failed read", snapshot.Bytes)
	}
}

func TestCountersZeroActivityIsUsable(t *testing.T) {
	var c Counters

	snapshot := c.Snapshot()
	if snapshot.Ops != 0 || snapshot.Errors != 0 || snapshot.Bytes != 0 || snapshot.MeanMS != 0 {
		t.Fatalf("zero-activity snapshot = %+v", snapshot)
	}
	if len(snapshot.Latency.BoundsMS) != len(latencyBoundsMS) {
		t.Fatalf("bounds length = %d, want %d", len(snapshot.Latency.BoundsMS), len(latencyBoundsMS))
	}
	if len(snapshot.Latency.Counts) != LatencyBucketCount {
		t.Fatalf("counts length = %d, want %d", len(snapshot.Latency.Counts), LatencyBucketCount)
	}
	if _, _, ok := snapshot.QuantileMS(0.95); ok {
		t.Fatal("quantile reported ok with no samples")
	}
}

func TestCountersLatencyBuckets(t *testing.T) {
	tests := []struct {
		elapsed time.Duration
		bucket  int
	}{
		{500 * time.Microsecond, 0}, // ≤ 1ms
		{time.Millisecond, 0},
		{1500 * time.Microsecond, 1}, // ≤ 2ms
		{3 * time.Millisecond, 2},    // ≤ 5ms
		{30 * time.Millisecond, 5},   // ≤ 50ms
		{120 * time.Millisecond, 7},  // ≤ 250ms
		{exactly(1000 * time.Millisecond), 9},
		{6 * time.Second, LatencyBucketCount - 1}, // overflow
	}

	for _, tt := range tests {
		if got := latencyBucket(tt.elapsed); got != tt.bucket {
			t.Fatalf("latencyBucket(%v) = %d, want %d", tt.elapsed, got, tt.bucket)
		}
	}
}

func TestCountersQuantilesAreMonotonicAndCountOverflow(t *testing.T) {
	var c Counters

	// 90 fast reads (≤1ms) and 10 slow ones above the last bound.
	for i := 0; i < 90; i++ {
		c.RecordRead(1, 500*time.Microsecond, nil)
	}
	for i := 0; i < 10; i++ {
		c.RecordRead(1, 30*time.Second, nil)
	}

	snapshot := c.Snapshot()
	if snapshot.Latency.Overflow != 10 {
		t.Fatalf("overflow = %d, want 10", snapshot.Latency.Overflow)
	}
	if got := snapshot.Latency.Samples(); got != 100 {
		t.Fatalf("samples = %d, want 100", got)
	}

	p50, exceed50, ok := snapshot.QuantileMS(0.50)
	if !ok || exceed50 || p50 != 1 {
		t.Fatalf("p50 = (%d, exceed=%v, ok=%v), want (1, false, true)", p50, exceed50, ok)
	}
	p95, exceed95, ok := snapshot.QuantileMS(0.95)
	if !ok || !exceed95 {
		t.Fatalf("p95 = (%d, exceed=%v, ok=%v), want the overflow bucket", p95, exceed95, ok)
	}
	p99, exceed99, _ := snapshot.QuantileMS(0.99)
	if p50 > p95 || p95 > p99 {
		t.Fatalf("quantiles not monotonic: p50=%d p95=%d p99=%d", p50, p95, p99)
	}
	if !exceed99 {
		t.Fatal("p99 should report exceeding the last bound")
	}
}

func TestCountersQuantileOnExactBoundary(t *testing.T) {
	var c Counters

	// Four samples: exactly 75% sit in the ≤1ms bucket, so q=0.75 must
	// resolve there rather than spilling into the next bucket.
	for i := 0; i < 3; i++ {
		c.RecordRead(1, time.Millisecond, nil)
	}
	c.RecordRead(1, 400*time.Millisecond, nil)

	snapshot := c.Snapshot()
	bound, exceed, ok := snapshot.QuantileMS(0.75)
	if !ok || exceed || bound != 1 {
		t.Fatalf("p75 = (%d, exceed=%v, ok=%v), want (1, false, true)", bound, exceed, ok)
	}
	bound, _, ok = snapshot.QuantileMS(0.76)
	if !ok || bound != 500 {
		t.Fatalf("p76 = (%d, ok=%v), want the 500ms bucket", bound, ok)
	}
}

func TestCountersRecordingDoesNotAllocate(t *testing.T) {
	c := &Counters{}
	elapsed := 20 * time.Millisecond
	allocs := testing.AllocsPerRun(1000, func() {
		c.RecordRead(4096, elapsed, nil)
	})
	if allocs != 0 {
		t.Fatalf("RecordRead allocated %v times per call, want 0", allocs)
	}
}

func TestCountersConcurrentRecordingIsExact(t *testing.T) {
	var c Counters

	const workers = 8
	const perWorker = 500
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				c.RecordRead(1024, 2*time.Millisecond, nil)
			}
		}()
	}
	wg.Wait()

	snapshot := c.Snapshot()
	if want := uint64(workers * perWorker); snapshot.Ops != want {
		t.Fatalf("ops = %d, want %d", snapshot.Ops, want)
	}
	if want := uint64(workers * perWorker * 1024); snapshot.Bytes != want {
		t.Fatalf("bytes = %d, want %d", snapshot.Bytes, want)
	}
	if got := snapshot.Latency.Samples(); got != uint64(workers*perWorker) {
		t.Fatalf("latency samples = %d, want %d", got, workers*perWorker)
	}
}

// exactly rounds a duration to the millisecond with the same ceiling the
// bucket lookup uses, so table entries can state their intent directly.
func exactly(d time.Duration) time.Duration {
	return (d + time.Millisecond - 1) / time.Millisecond * time.Millisecond
}
