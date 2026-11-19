package drive_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	testclock "github.com/yinzhenyu/qrypt/pkg/clock"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/testsupport/simread"
)

// TestSimReadSourceRetryDeterminism drives the production range retry and
// truncation replay logic through the simulated provider twice and requires
// both runs to produce identical bytes and an identical request log. It is the
// rerun-determinism gate for the read path: same scripted faults in, same
// number of range attempts out, with no wall-clock-dependent divergence.
func TestSimReadSourceRetryDeterminism(t *testing.T) {
	size := int64(drive.ReadChunkSize)
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 251)
	}

	run := func(t *testing.T) ([]byte, []simread.Request) {
		t.Helper()
		base := drive.NewFakeDriver()
		if seedErr := base.Seed(map[string]string{"file.bin": string(content)}); seedErr != nil {
			t.Fatalf("seed fake driver: %v", seedErr)
		}
		sim := simread.Wrap(base, simread.Config{Faults: []simread.Fault{
			{Status: 503},
			{Status: 429},
			{Truncate: true},
		}})
		source, err := drive.NewReadSource(sim, nil)
		if err != nil {
			t.Fatalf("new read source: %v", err)
		}
		clk := testclock.NewFake(time.Unix(1_700_000_000, 0))
		ctx, cancel := context.WithTimeout(drive.WithClock(context.Background(), clk), 30*time.Second)
		defer cancel()
		done := make(chan struct{})
		go pumpFakeClock(clk, 10*time.Millisecond, 400, done)
		entry := drive.Entry{ID: "0/file.bin", Name: "file.bin", Size: size}
		data, err := source.Fetch(ctx, entry, drive.RangePlan{Offset: 0, Size: size, Streams: 1})
		close(done)
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		return data, sim.Requests()
	}

	first, firstLog := run(t)
	second, secondLog := run(t)
	if !bytes.Equal(first, content) {
		t.Fatalf("first run returned %d bytes, want the full %d", len(first), size)
	}
	if !bytes.Equal(second, content) {
		t.Fatalf("second run returned %d bytes, want the full %d", len(second), size)
	}

	// 503, 429, one truncated body, then a clean retry: four attempts per run.
	wantStatus := []int{503, 429, 0, 0}
	wantTruncated := []bool{false, false, true, false}
	wantDelivered := []int64{0, 0, size / 2, size}
	wantAttempt := []int{1, 2, 3, 4}
	if len(firstLog) != len(wantStatus) {
		t.Fatalf("recorded %d attempts, want %d: %+v", len(firstLog), len(wantStatus), firstLog)
	}
	for i, req := range firstLog {
		if req.Offset != 0 || req.Size != size {
			t.Fatalf("attempt %d range = [%d,%d), want [0,%d)", i+1, req.Offset, req.Offset+req.Size, size)
		}
		if req.Status != wantStatus[i] || req.Truncated != wantTruncated[i] ||
			req.Delivered != wantDelivered[i] || req.Attempt != wantAttempt[i] {
			t.Fatalf("attempt %d = %+v, want status %d truncated %t delivered %d attempt %d",
				i+1, req, wantStatus[i], wantTruncated[i], wantDelivered[i], wantAttempt[i])
		}
	}

	if len(firstLog) != len(secondLog) {
		t.Fatalf("rerun recorded %d attempts, first run %d", len(secondLog), len(firstLog))
	}
	for i := range firstLog {
		a, b := firstLog[i], secondLog[i]
		if a.Offset != b.Offset || a.Size != b.Size || a.Attempt != b.Attempt ||
			a.Status != b.Status || a.Truncated != b.Truncated || a.Delivered != b.Delivered ||
			a.Concurrent != b.Concurrent || a.Err != b.Err {
			t.Fatalf("rerun attempt %d diverged:\nfirst:  %+v\nsecond: %+v", i+1, a, b)
		}
	}
}
