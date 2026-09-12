package drive

import (
	"sync/atomic"
	"time"
)

// latencyBoundsMS are the inclusive upper bounds of the latency histogram
// buckets, in milliseconds. Samples above the last bound land in the overflow
// bucket, so the histogram has len(latencyBoundsMS)+1 buckets in total.
//
// The range starts at the sub-millisecond floor and ends at five seconds:
// cloud-drive reads that resolve through the cache sit in the low buckets,
// while a cold remote window load lands in the high ones.
var latencyBoundsMS = [12]int64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

// LatencyBucketCount is the number of histogram buckets, including overflow.
const LatencyBucketCount = len(latencyBoundsMS) + 1

// Counters is a lock-free per-mount counter set: cumulative totals plus a
// fixed-bucket latency histogram. Recording is a handful of atomic adds and
// allocates nothing, so it can run on every operation; the storage it uses is
// constant no matter how many operations are recorded.
//
// The totals are cumulative rather than windowed, so they answer "how fast and
// how reliable overall" while the bounded event histories answer "which
// operation, and where did it spend its time".
type Counters struct {
	ops        atomic.Uint64
	errors     atomic.Uint64
	bytes      atomic.Uint64
	durationNS atomic.Uint64
	latency    [LatencyBucketCount]atomic.Uint64
}

// RecordRead counts one read attempt. bytes is the payload the read path
// materialized (0 when it hands back a stream, matching the read event's Bytes
// field), elapsed is the duration of the read call, and err is non-nil for
// failures. Failed attempts count toward ops, errors and the latency
// histogram, but never toward bytes.
func (c *Counters) RecordRead(bytes int64, elapsed time.Duration, err error) {
	if c == nil {
		return
	}
	c.ops.Add(1)
	if err != nil {
		c.errors.Add(1)
	} else if bytes > 0 {
		c.bytes.Add(uint64(bytes))
	}
	if elapsed > 0 {
		c.durationNS.Add(uint64(elapsed))
	}
	c.latency[latencyBucket(elapsed)].Add(1)
}

// LatencyHistogram is the JSON shape of the latency distribution: the bucket
// bounds so consumers can interpret the counts, the counts themselves, and the
// number of samples above the last bound.
type LatencyHistogram struct {
	BoundsMS []int64  `json:"bounds_ms"`
	Counts   []uint64 `json:"counts"`
	Overflow uint64   `json:"overflow"`
}

// CounterSnapshot is the JSON shape of one mount's counters at a point in
// time. Ops, Errors and Bytes are cumulative since process start, not windowed.
type CounterSnapshot struct {
	Ops        uint64           `json:"ops"`
	Errors     uint64           `json:"errors"`
	Bytes      uint64           `json:"bytes"`
	DurationMS int64            `json:"duration_ms"`
	MeanMS     float64          `json:"mean_ms"`
	Latency    LatencyHistogram `json:"latency"`
}

// Snapshot returns the current totals and histogram. Zero-activity mounts
// report zeros with an all-zero histogram rather than nil latency data.
func (c *Counters) Snapshot() CounterSnapshot {
	if c == nil {
		return CounterSnapshot{Latency: emptyHistogram()}
	}
	ops := c.ops.Load()
	snapshot := CounterSnapshot{
		Ops:        ops,
		Errors:     c.errors.Load(),
		Bytes:      c.bytes.Load(),
		DurationMS: durationMillis(time.Duration(c.durationNS.Load())),
		Latency:    emptyHistogram(),
	}
	if ops > 0 {
		snapshot.MeanMS = float64(snapshot.DurationMS) / float64(ops)
	}
	for i := range snapshot.Latency.Counts {
		snapshot.Latency.Counts[i] = c.latency[i].Load()
	}
	snapshot.Latency.Overflow = snapshot.Latency.Counts[len(latencyBoundsMS)]
	return snapshot
}

// Samples reports how many latency samples the histogram holds.
func (h LatencyHistogram) Samples() uint64 {
	var total uint64
	for _, count := range h.Counts {
		total += count
	}
	return total
}

// QuantileMS returns the upper bound (in milliseconds) of the bucket holding
// the q-th percentile of the recorded latencies. When the percentile falls in
// the overflow bucket the last bound is returned with exceed set, meaning the
// true latency is above it. ok is false when no sample was recorded.
//
// The returned value is a bucket bound, not an interpolated estimate: callers
// get "p95 is at most this", which is the honest reading of a histogram.
func (s CounterSnapshot) QuantileMS(q float64) (boundMS int64, exceed bool, ok bool) {
	total := s.Latency.Samples()
	if total == 0 {
		return 0, false, false
	}
	if q <= 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}
	// Smallest cumulative count that satisfies the percentile; integer
	// arithmetic keeps the threshold exact for a sample count that is not
	// divisible by 100.
	threshold := uint64(q * float64(total))
	if float64(threshold) < q*float64(total) {
		threshold++
	}
	if threshold == 0 {
		threshold = 1
	}
	var cumulative uint64
	for i, count := range s.Latency.Counts {
		cumulative += count
		if cumulative < threshold {
			continue
		}
		if i >= len(latencyBoundsMS) {
			return latencyBoundsMS[len(latencyBoundsMS)-1], true, true
		}
		return s.Latency.BoundsMS[i], false, true
	}
	// Unreachable while Counts is at least as long as the bounds, but keeps
	// the contract total over the histogram.
	return latencyBoundsMS[len(latencyBoundsMS)-1], true, true
}

func emptyHistogram() LatencyHistogram {
	bounds := make([]int64, len(latencyBoundsMS))
	copy(bounds, latencyBoundsMS[:])
	return LatencyHistogram{
		BoundsMS: bounds,
		Counts:   make([]uint64, LatencyBucketCount),
	}
}

func latencyBucket(elapsed time.Duration) int {
	// Ceiling milliseconds: a sample counts in the first bucket it does not
	// exceed, so a 1.5ms read is "at most 2ms" rather than "at most 1ms".
	ms := int64(0)
	if elapsed > 0 {
		ms = int64((elapsed + time.Millisecond - 1) / time.Millisecond)
	}
	for i, bound := range latencyBoundsMS {
		if ms <= bound {
			return i
		}
	}
	return len(latencyBoundsMS)
}
