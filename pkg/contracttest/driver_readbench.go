package contracttest

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// DefaultReadBenchSize is large enough to amortise per-request latency over
// several concurrent ranges.
const DefaultReadBenchSize = 64 << 20

// DefaultReadBenchConcurrency is the concurrency sweep used when the caller
// does not pass one.
var DefaultReadBenchConcurrency = []int{1, 2, 4, 8}

// ReadBenchSample is one (concurrency, sample) measurement of raw driver reads.
type ReadBenchSample struct {
	Concurrency  int    `json:"concurrency"`
	Bytes        int64  `json:"bytes"`
	DurationUS   int64  `json:"duration_us"`
	AggregateBPS int64  `json:"aggregate_bps"`
	PerStreamBPS int64  `json:"per_stream_bps,omitempty"`
	Errors       int    `json:"errors,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ReadBenchReport is the raw-driver read throughput matrix for one mount.
type ReadBenchReport struct {
	Mount        string            `json:"mount"`
	Driver       string            `json:"driver,omitempty"`
	FileSize     int64             `json:"file_size"`
	Samples      []ReadBenchSample `json:"samples"`
	CleanupError string            `json:"cleanup_error,omitempty"`
	Error        string            `json:"error,omitempty"`
}

// ParseConcurrencyList parses a comma-separated concurrency sweep such as
// "1,2,4,8". Empty input returns DefaultReadBenchConcurrency.
func ParseConcurrencyList(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return append([]int(nil), DefaultReadBenchConcurrency...), nil
	}
	seen := map[int]bool{}
	var levels []int
	for _, part := range strings.Split(raw, ",") {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("concurrency %q is not a number", part)
		}
		if value < 1 || value > 32 {
			return nil, fmt.Errorf("concurrency %d must be between 1 and 32", value)
		}
		if !seen[value] {
			seen[value] = true
			levels = append(levels, value)
		}
	}
	if len(levels) == 0 {
		return nil, fmt.Errorf("concurrency list is empty")
	}
	sort.Ints(levels)
	return levels, nil
}

// RunDriverReadBenchmark measures driver.Read throughput at several concurrency
// levels, bypassing the VFS and encryption layers. It answers whether the
// provider scales with parallel ranges at all, independently of qrypt's read
// pipeline. The fixture file is created and removed by the benchmark.
func RunDriverReadBenchmark(ctx context.Context, mount string, d drive.Driver, size int64, concurrency []int, samples int, interval time.Duration) *ReadBenchReport {
	report := &ReadBenchReport{Mount: mount, FileSize: size}
	if size <= 0 {
		size = DefaultReadBenchSize
		report.FileSize = size
	}
	if size > MaxUploadProbeSize {
		report.Error = fmt.Sprintf("read benchmark size %d exceeds the %d-byte limit", size, int64(MaxUploadProbeSize))
		return report
	}
	if len(concurrency) == 0 {
		concurrency = DefaultReadBenchConcurrency
	}
	if samples <= 0 {
		samples = 1
	}
	if snap, err := d.DebugSnapshot(ctx); err == nil {
		report.Driver = snap.Driver
	}
	for _, capability := range []drive.Capability{drive.CapabilityWriter, drive.CapabilitySourceUploader} {
		if !drive.HasCapability(d, capability) {
			report.Error = fmt.Sprintf("driver lacks capability %s required to build the benchmark fixture", capability)
			return report
		}
	}

	fx, err := NewFixture(ctx, d, "readbench")
	if err != nil {
		report.Error = fmt.Sprintf("create benchmark fixture: %v", err)
		return report
	}
	defer func() {
		if err := fx.d.Remove(ctx, fx.TestDir); err != nil && report.CleanupError == "" {
			report.CleanupError = err.Error()
		}
	}()
	entry, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: fx.TestDir.ID,
		Name:     "payload.txt",
		Source:   newPatternFileSource(size),
	})
	if err != nil {
		report.Error = fmt.Sprintf("upload benchmark fixture: %v", err)
		return report
	}
	defer func() {
		if err := fx.d.Remove(ctx, entry); err != nil && report.CleanupError == "" {
			report.CleanupError = err.Error()
		}
	}()

	for _, level := range concurrency {
		for sample := 0; sample < samples; sample++ {
			if (len(report.Samples) > 0) && interval > 0 {
				timer := time.NewTimer(interval)
				select {
				case <-ctx.Done():
					timer.Stop()
					report.Error = ctx.Err().Error()
					return report
				case <-timer.C:
				}
			}
			started := time.Now()
			read, errors, readErr := readRangeConcurrent(ctx, d, entry, size, level)
			elapsed := time.Since(started)
			report.Samples = append(report.Samples, ReadBenchSample{
				Concurrency:  level,
				Bytes:        read,
				DurationUS:   int64(elapsed / time.Microsecond),
				AggregateBPS: bytesPerSecond(read, elapsed),
				PerStreamBPS: bytesPerSecond(read, elapsed) / int64(level),
				Errors:       errors,
				Error:        errorText(readErr),
			})
			if readErr != nil {
				return report
			}
		}
	}
	return report
}

// readRangeConcurrent divides the file into one range per stream and reads all
// ranges at once.
func readRangeConcurrent(ctx context.Context, d drive.Driver, entry drive.Entry, size int64, concurrency int) (int64, int, error) {
	rangeSize := size / int64(concurrency)
	if rangeSize <= 0 {
		return 0, 0, fmt.Errorf("file size %d is too small for %d streams", size, concurrency)
	}
	var total atomic.Int64
	var failures atomic.Int32
	var mu sync.Mutex
	var firstErr error
	setErr := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		offset := int64(i) * rangeSize
		length := rangeSize
		if i == concurrency-1 {
			length = size - offset
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc, err := d.Read(ctx, entry, offset, length)
			if err != nil {
				failures.Add(1)
				setErr(err)
				return
			}
			n, err := io.Copy(io.Discard, rc)
			closeErr := rc.Close()
			total.Add(n)
			if err != nil {
				failures.Add(1)
				setErr(err)
			} else if closeErr != nil {
				failures.Add(1)
				setErr(closeErr)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	err := firstErr
	mu.Unlock()
	return total.Load(), int(failures.Load()), err
}
