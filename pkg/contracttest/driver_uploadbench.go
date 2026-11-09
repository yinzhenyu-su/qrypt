package contracttest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// maxUploadBenchSampleBytes bounds one measurement so a typo cannot start a
// multi-terabyte sweep.
const maxUploadBenchSampleBytes = int64(16 << 30)

// UploadBenchSample is one (chunk size, concurrency, sample) measurement.
type UploadBenchSample struct {
	ChunkSize    int64  `json:"chunk_size"` // requested; 0 = driver default
	Concurrency  int    `json:"concurrency"`
	Bytes        int64  `json:"bytes"`
	DurationUS   int64  `json:"duration_us"`
	AggregateBPS int64  `json:"aggregate_bps"`
	PerStreamBPS int64  `json:"per_stream_bps,omitempty"`
	Errors       int    `json:"errors,omitempty"`
	Error        string `json:"error,omitempty"`
}

// UploadBenchReport is the upload throughput matrix for one mount.
type UploadBenchReport struct {
	Mount             string              `json:"mount"`
	Driver            string              `json:"driver,omitempty"`
	FileSize          int64               `json:"file_size"`
	SupportsChunkSize bool                `json:"supports_chunk_size"`
	Unsupported       []string            `json:"unsupported,omitempty"`
	Samples           []UploadBenchSample `json:"samples"`
	CleanupError      string              `json:"cleanup_error,omitempty"`
	Error             string              `json:"error,omitempty"`
}

// ParseChunkSizeList parses a comma-separated chunk-size sweep such as
// "4m,8m,16m". Empty input means "keep the driver default" and returns [0].
func ParseChunkSizeList(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []int64{0}, nil
	}
	var sizes []int64
	for _, part := range strings.Split(raw, ",") {
		size := ParseXferSize(part)
		if size < 1 {
			return nil, fmt.Errorf("chunk size %q is invalid", part)
		}
		if size > MaxUploadProbeSize {
			return nil, fmt.Errorf("chunk size %q exceeds %d bytes", part, int64(MaxUploadProbeSize))
		}
		sizes = append(sizes, size)
	}
	if len(sizes) == 0 {
		return nil, fmt.Errorf("chunk size list is empty")
	}
	return sizes, nil
}

// RunDriverUploadBenchmark uploads generated fixtures at several concurrency
// and chunk-size levels and reports aggregate throughput. Concurrency means
// simultaneous uploads (threads); chunk size is applied through the driver's
// upload profile when it supports one. The fixture is always removed.
func RunDriverUploadBenchmark(ctx context.Context, mount string, d drive.Driver, size int64, concurrency []int, chunkSizes []int64, samples int, interval time.Duration) *UploadBenchReport {
	report := &UploadBenchReport{Mount: mount, FileSize: size}
	if size <= 0 {
		size = DefaultReadBenchSize
		report.FileSize = size
	}
	if len(concurrency) == 0 {
		concurrency = DefaultReadBenchConcurrency
	}
	if len(chunkSizes) == 0 {
		chunkSizes = []int64{0}
	}
	if samples <= 0 {
		samples = 1
	}
	if snap, err := d.DebugSnapshot(ctx); err == nil {
		report.Driver = snap.Driver
	}
	support := drive.UploadProfile{}
	if profiler, ok := d.(drive.UploadProfiler); ok {
		support = profiler.UploadProfile()
	}
	report.SupportsChunkSize = support.ChunkSize > 0
	for _, chunkSize := range chunkSizes {
		if chunkSize > 0 && !report.SupportsChunkSize {
			report.Unsupported = append(report.Unsupported, "chunk_size")
			break
		}
	}
	if report.SupportsChunkSize && len(chunkSizes) == 1 && chunkSizes[0] == 0 {
		// No sweep requested: measure the driver default once.
		chunkSizes = []int64{support.ChunkSize}
	}
	for _, capability := range []drive.Capability{drive.CapabilityWriter, drive.CapabilitySourceUploader} {
		if !drive.HasCapability(d, capability) {
			report.Error = fmt.Sprintf("driver lacks capability %s required for upload benchmarking", capability)
			return report
		}
	}

	fx, err := NewFixture(ctx, d, "uploadbench")
	if err != nil {
		report.Error = fmt.Sprintf("create benchmark fixture: %v", err)
		return report
	}
	defer func() {
		if err := fx.d.Remove(ctx, fx.TestDir); err != nil && report.CleanupError == "" {
			report.CleanupError = err.Error()
		}
	}()

	for _, chunkSize := range chunkSizes {
		if chunkSize > 0 && !report.SupportsChunkSize {
			// Reported once above; measure the default instead of failing the
			// whole sweep.
			chunkSize = 0
		}
		for _, level := range concurrency {
			if total := size * int64(level); total > maxUploadBenchSampleBytes {
				report.Error = fmt.Sprintf("%d concurrent uploads of %d bytes exceed the %d-byte sample limit", level, size, maxUploadBenchSampleBytes)
				return report
			}
			for sample := 0; sample < samples; sample++ {
				if len(report.Samples) > 0 && interval > 0 {
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
				uploaded, errors, uploadErr := uploadConcurrent(ctx, d, fx.TestDir.ID, size, level, chunkSize, fmt.Sprintf("%d-%d-%d", chunkSize, level, sample))
				elapsed := time.Since(started)
				report.Samples = append(report.Samples, UploadBenchSample{
					ChunkSize:    chunkSize,
					Concurrency:  level,
					Bytes:        uploaded,
					DurationUS:   int64(elapsed / time.Microsecond),
					AggregateBPS: bytesPerSecond(uploaded, elapsed),
					PerStreamBPS: bytesPerSecond(uploaded, elapsed) / int64(level),
					Errors:       errors,
					Error:        errorText(uploadErr),
				})
				if uploadErr != nil {
					return report
				}
			}
		}
	}
	return report
}

// uploadConcurrent starts one PutSource per thread with the same size and
// chunk override. Each fixture is removed before the function returns.
func uploadConcurrent(ctx context.Context, d drive.Driver, parentID string, size int64, level int, chunkSize int64, tag string) (int64, int, error) {
	var total atomic.Int64
	var failures atomic.Int32
	var mu sync.Mutex
	var firstErr error
	var entries []drive.Entry
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
	for i := 0; i < level; i++ {
		uploadCtx := ctx
		if chunkSize > 0 {
			uploadCtx = drive.WithUploadProfile(uploadCtx, drive.UploadProfile{ChunkSize: chunkSize})
		}
		name := fmt.Sprintf("payload-%s-%d.txt", tag, i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Unique bytes per thread: identical content could be deduplicated
			// by drivers with instant upload, which would measure the
			// dedup path instead of the transfer.
			seed := int64(chunkSize%9973) + int64(level)*7919 + int64(i) + 1
			entry, err := d.PutSource(uploadCtx, drive.UploadRequest{
				ParentID: parentID,
				Name:     name,
				Source:   newSeededPatternFileSource(size, seed),
			})
			if err != nil {
				failures.Add(1)
				setErr(err)
				return
			}
			total.Add(size)
			mu.Lock()
			entries = append(entries, entry)
			mu.Unlock()
		}()
	}
	wg.Wait()

	for _, entry := range entries {
		if err := d.Remove(ctx, entry); err != nil {
			setErr(fmt.Errorf("remove benchmark file %q: %w", entry.Name, err))
		}
	}
	mu.Lock()
	err := firstErr
	mu.Unlock()
	return total.Load(), int(failures.Load()), err
}
