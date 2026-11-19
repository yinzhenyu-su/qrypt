package simread_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/testsupport/simread"
)

// newLocalfsFixture writes a deterministic file under a temporary directory
// and returns a localfs driver over it plus the matching entry and content.
func newLocalfsFixture(t *testing.T, size int64) (*localfs.Driver, drive.Entry, []byte) {
	t.Helper()
	dir := t.TempDir()
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 251)
	}
	path := filepath.Join(dir, "file.bin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	d := localfs.New(dir)
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("init localfs: %v", err)
	}
	entry := drive.Entry{ID: path, ParentID: dir, Name: "file.bin", Size: size}
	return d, entry, content
}

func readAll(t *testing.T, p *simread.Provider, entry drive.Entry, offset, size int64) ([]byte, error) {
	t.Helper()
	body, err := p.Read(context.Background(), entry, offset, size)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := body.Close(); err != nil {
			t.Fatalf("close body: %v", err)
		}
	}()
	return io.ReadAll(body)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSimDeliversPacedContent(t *testing.T) {
	const size = 128 << 10
	d, entry, content := newLocalfsFixture(t, size)
	p := simread.Wrap(d, simread.Config{RateBytesPerSec: 256 << 10, TimeScale: 8})

	start := time.Now()
	got, err := readAll(t, p, entry, 0, size)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: got %d bytes", len(got))
	}
	// 128 KiB at 256 KiB/s is 0.5 simulated seconds; at scale 8 that is ~63 ms
	// of real time. Only generous bounds are asserted so the test stays stable
	// on a loaded machine.
	if elapsed < 30*time.Millisecond {
		t.Fatalf("paced read finished in %s, expected pacing to take time", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("paced read took %s, expected compressed pacing", elapsed)
	}
	stats := p.Stats()
	if stats.Requests != 1 || stats.BytesServed != size {
		t.Fatalf("stats = %+v, want 1 request / %d bytes", stats, size)
	}
	reqs := p.Requests()
	if len(reqs) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Offset != 0 || req.Size != size || req.Attempt != 1 || req.Concurrent != 1 || req.Delivered != size {
		t.Fatalf("request = %+v, want offset 0 size %d attempt 1 delivered %d", req, size, size)
	}
	if req.Err != "" || req.Finished.IsZero() {
		t.Fatalf("request not finished cleanly: %+v", req)
	}
}

func TestSimTimeScaleCompressesPacing(t *testing.T) {
	const size = 64 << 10
	const rate = 256 << 10 // 0.25 simulated seconds for the whole file.
	d, entry, content := newLocalfsFixture(t, size)

	measure := func(scale float64) time.Duration {
		p := simread.Wrap(d, simread.Config{RateBytesPerSec: rate, TimeScale: scale})
		start := time.Now()
		got, err := readAll(t, p, entry, 0, size)
		if err != nil {
			t.Fatalf("read at scale %.1f: %v", scale, err)
		}
		if !bytes.Equal(got, content) {
			t.Fatalf("content mismatch at scale %.1f", scale)
		}
		return time.Since(start)
	}

	realTime := measure(1)
	compressed := measure(8)
	if realTime < 200*time.Millisecond {
		t.Fatalf("unscaled pacing took %s, want >= 200ms for 0.25 simulated seconds", realTime)
	}
	if compressed > 150*time.Millisecond {
		t.Fatalf("scale-8 pacing took %s, want compressed (<= 150ms)", compressed)
	}
}

func TestSimScriptsStatusAndTruncatedFaults(t *testing.T) {
	const size = 64 << 10
	d, entry, content := newLocalfsFixture(t, size)
	p := simread.Wrap(d, simread.Config{Faults: []simread.Fault{
		{Status: 503},
		{Status: 429},
		{Truncate: true},
	}})

	for i, want := range []int{503, 429} {
		if _, err := p.Read(context.Background(), entry, 0, size); err == nil {
			t.Fatalf("attempt %d: expected status %d error", i+1, want)
		} else {
			var statusErr *drive.HTTPStatusError
			if !errors.As(err, &statusErr) || statusErr.Status != want {
				t.Fatalf("attempt %d: error = %v, want HTTP %d", i+1, err, want)
			}
		}
	}

	short, err := readAll(t, p, entry, 0, size)
	if err != nil {
		t.Fatalf("truncated attempt: %v", err)
	}
	if len(short) != size/2 {
		t.Fatalf("truncated attempt delivered %d bytes, want %d", len(short), size/2)
	}
	if !bytes.Equal(short, content[:size/2]) {
		t.Fatalf("truncated attempt delivered wrong bytes")
	}

	full, err := readAll(t, p, entry, 0, size)
	if err != nil {
		t.Fatalf("recovered attempt: %v", err)
	}
	if !bytes.Equal(full, content) {
		t.Fatalf("recovered attempt delivered %d bytes, want %d", len(full), size)
	}

	reqs := p.Requests()
	wantStatus := []int{503, 429, 0, 0}
	wantTruncated := []bool{false, false, true, false}
	wantDelivered := []int64{0, 0, size / 2, size}
	wantAttempt := []int{1, 2, 3, 4}
	if len(reqs) != len(wantStatus) {
		t.Fatalf("recorded %d requests, want %d", len(reqs), len(wantStatus))
	}
	for i, req := range reqs {
		if req.Status != wantStatus[i] || req.Truncated != wantTruncated[i] ||
			req.Delivered != wantDelivered[i] || req.Attempt != wantAttempt[i] {
			t.Fatalf("request %d = %+v, want status %d truncated %t delivered %d attempt %d",
				i, req, wantStatus[i], wantTruncated[i], wantDelivered[i], wantAttempt[i])
		}
	}
}

func TestSimOpenLatencyAndTTFB(t *testing.T) {
	const size = 4 << 10
	d, entry, content := newLocalfsFixture(t, size)
	p := simread.Wrap(d, simread.Config{
		OpenLatency: 12 * time.Millisecond,
		TTFB:        18 * time.Millisecond,
	})

	start := time.Now()
	body, err := p.Read(context.Background(), entry, 0, size)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	elapsed := time.Since(start)
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch")
	}
	// 12ms open latency + 18ms TTFB = 30ms simulated before the first byte.
	if elapsed < 25*time.Millisecond {
		t.Fatalf("read with latency took %s, want >= 25ms", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("read with latency took %s, too slow", elapsed)
	}
}

func TestSimMidBodyStall(t *testing.T) {
	const size = 128 << 10
	d, entry, content := newLocalfsFixture(t, size)
	p := simread.Wrap(d, simread.Config{Stall: simread.Stall{AfterBytes: 64 << 10, For: 40 * time.Millisecond}})

	start := time.Now()
	got, err := readAll(t, p, entry, 0, size)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("stalled read delivered %d bytes, want %d", len(got), size)
	}
	if elapsed < 35*time.Millisecond {
		t.Fatalf("read finished in %s, expected a mid-body stall", elapsed)
	}
}

func TestSimMaxConnectionsCap(t *testing.T) {
	const size = 64 << 10
	const bodyCount = 6
	d, entry, content := newLocalfsFixture(t, size)
	gate := make(chan struct{})
	p := simread.Wrap(d, simread.Config{
		MaxConnections: 2,
		Gate:           func(offset, size int64) <-chan struct{} { return gate },
	})

	errs := make(chan error, bodyCount)
	var wg sync.WaitGroup
	for i := 0; i < bodyCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, err := p.Read(context.Background(), entry, 0, size)
			if err != nil {
				errs <- err
				return
			}
			defer body.Close()
			got, err := io.ReadAll(body)
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(got, content) {
				errs <- fmt.Errorf("content mismatch: got %d bytes", len(got))
			}
		}()
	}

	waitFor(t, "two gated connections", func() bool { return p.Stats().Active == 2 })
	if stats := p.Stats(); stats.Requests != 2 || stats.MaxConcurrent > 2 {
		t.Fatalf("stats while gated = %+v, want 2 active requests capped at 2", stats)
	}
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("read: %v", err)
	}
	stats := p.Stats()
	if stats.Requests != bodyCount || stats.MaxConcurrent != 2 || stats.Active != 0 {
		t.Fatalf("stats = %+v, want %d requests, peak concurrency 2, none active", stats, bodyCount)
	}
	if stats.BytesServed != bodyCount*size {
		t.Fatalf("bytes served = %d, want %d", stats.BytesServed, bodyCount*size)
	}
}

func TestSimCapabilitiesAndPolicy(t *testing.T) {
	base := drive.NewFakeDriver()
	if seedErr := base.Seed(map[string]string{"file.bin": "hello"}); seedErr != nil {
		t.Fatalf("seed fake driver: %v", seedErr)
	}
	p := simread.Wrap(base, simread.Config{})
	caps := p.Capabilities()
	found := false
	for _, c := range caps {
		if c == drive.CapabilityParallelRange {
			found = true
		}
	}
	if !found {
		t.Fatalf("capabilities = %v, want %s added", caps, drive.CapabilityParallelRange)
	}
	if got, want := p.ReadPolicy(), drive.DefaultReadPolicy(); got != want {
		t.Fatalf("policy = %+v, want default %+v", got, want)
	}
}

func TestSimResetClearsLog(t *testing.T) {
	d, entry, _ := newLocalfsFixture(t, 8<<10)
	p := simread.Wrap(d, simread.Config{})
	if _, err := readAll(t, p, entry, 0, 8<<10); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := len(p.Requests()); got != 1 {
		t.Fatalf("recorded %d requests, want 1", got)
	}
	p.Reset()
	if got := len(p.Requests()); got != 0 {
		t.Fatalf("recorded %d requests after reset, want 0", got)
	}
	if got := p.Stats().BytesServed; got != 0 {
		t.Fatalf("bytes served after reset = %d, want 0", got)
	}
}
