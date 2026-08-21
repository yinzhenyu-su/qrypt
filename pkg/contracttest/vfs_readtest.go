package contracttest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const (
	DefaultMountedReadTestSize      = 256 << 20
	DefaultMountedReadTestBlockSize = 1 << 20
	MaxMountedReadTestSize          = 2 << 30
	MaxMountedReadTestSamples       = 10
)

type MountedReadTestResult struct {
	OpID             string                   `json:"op_id"`
	Mount            string                   `json:"mount"`
	Pass             bool                     `json:"pass"`
	MountPoint       string                   `json:"mount_point"`
	VirtualPath      string                   `json:"virtual_path"`
	Size             int64                    `json:"size"`
	BlockSize        int64                    `json:"block_size"`
	CacheMode        string                   `json:"cache_mode"`
	Samples          int                      `json:"samples"`
	Measurements     []MountedReadMeasurement `json:"measurements"`
	Summary          MountedReadSummary       `json:"summary"`
	Steps            []FSTestStep             `json:"steps"`
	Metrics          []drive.MetricEvent      `json:"metrics,omitempty"`
	MetricsTruncated bool                     `json:"metrics_truncated,omitempty"`
	Started          time.Time                `json:"started_at"`
	Finished         time.Time                `json:"finished_at"`
	Duration         string                   `json:"duration"`
	DurationMS       int64                    `json:"duration_ms"`
	RetryCommand     string                   `json:"retry_command,omitempty"`
}

type MountedReadDetails struct {
	MountPoint   string                   `json:"mount_point"`
	VirtualPath  string                   `json:"virtual_path"`
	Size         int64                    `json:"size"`
	BlockSize    int64                    `json:"block_size"`
	CacheMode    string                   `json:"cache_mode"`
	Samples      int                      `json:"samples"`
	Measurements []MountedReadMeasurement `json:"measurements"`
	Summary      MountedReadSummary       `json:"summary"`
}

type MountedReadMeasurement struct {
	Mode           string             `json:"mode"`
	Sample         int                `json:"sample"`
	Bytes          int64              `json:"bytes"`
	OpenMicros     int64              `json:"open_us"`
	TTFBMicros     int64              `json:"ttfb_us"`
	DurationMicros int64              `json:"duration_us"`
	EndToEndBPS    int64              `json:"end_to_end_bps"`
	SteadyBPS      int64              `json:"steady_bps"`
	PeakWindowBPS  int64              `json:"peak_1s_bps"`
	ReadCalls      int                `json:"read_calls"`
	ReadLatency    MountedReadLatency `json:"read_latency_us"`
	CacheHits      int64              `json:"cache_hits"`
	CacheMisses    int64              `json:"cache_misses"`
	VFSReadCalls   int                `json:"vfs_read_calls"`
	TraversedVFS   bool               `json:"traversed_vfs"`
	OSCacheControl string             `json:"os_cache_control"`
	SHA256         string             `json:"sha256"`
	ContentMatch   bool               `json:"content_match"`
}

type MountedReadLatency struct {
	P50 int64 `json:"p50"`
	P95 int64 `json:"p95"`
	P99 int64 `json:"p99"`
	Max int64 `json:"max"`
}

type MountedReadModeSummary struct {
	Samples          int   `json:"samples"`
	MedianTTFBMicros int64 `json:"median_ttfb_us"`
	P95TTFBMicros    int64 `json:"p95_ttfb_us"`
	MedianBPS        int64 `json:"median_bps"`
	MaxBPS           int64 `json:"max_bps"`
	PeakWindowBPS    int64 `json:"peak_1s_bps"`
}

type MountedReadSummary struct {
	Cold *MountedReadModeSummary `json:"cold,omitempty"`
	Warm *MountedReadModeSummary `json:"warm,omitempty"`
}

func RunVFSMountedReadTest(ctx context.Context, fs vfs.FileSystem, mount string, req DriverTestRequest) *MountedReadTestResult {
	size := ParseXferSize(req.Size)
	var optionErr error
	if req.Size != "" && (size < 1 || size > MaxMountedReadTestSize) {
		optionErr = fmt.Errorf("read test size must be between 1 and %d bytes", MaxMountedReadTestSize)
	}
	if size <= 0 {
		size = DefaultMountedReadTestSize
	}
	blockSize := ParseXferSize(req.BlockSize)
	if req.BlockSize != "" && (blockSize < 1 || blockSize > 16<<20) {
		optionErr = fmt.Errorf("read test block size must be between 1 and %d bytes", 16<<20)
	}
	if blockSize <= 0 {
		blockSize = DefaultMountedReadTestBlockSize
	}
	samples := req.Samples
	if samples <= 0 {
		samples = 1
	}
	if samples > MaxMountedReadTestSamples {
		optionErr = fmt.Errorf("read test samples must be between 1 and %d", MaxMountedReadTestSamples)
	}
	cacheMode := strings.ToLower(strings.TrimSpace(req.CacheMode))
	if cacheMode == "" {
		cacheMode = "both"
	}
	if cacheMode != "cold" && cacheMode != "warm" && cacheMode != "both" {
		optionErr = fmt.Errorf("read test cache mode must be cold, warm, or both")
	}
	result := &MountedReadTestResult{
		OpID: newDebugOperationID("read"), Mount: mount, MountPoint: req.MountPoint,
		Size: size, BlockSize: blockSize, CacheMode: cacheMode, Samples: samples,
		Started: time.Now(), Steps: make([]FSTestStep, 0, 4+samples*2),
		RetryCommand: fmt.Sprintf("qrypt debug test read --mount %s --mount-point PATH --size %d --block-size %d --cache-mode %s --samples %d --socket PATH", mount, size, blockSize, cacheMode, samples),
	}
	defer func() {
		result.Finished = time.Now()
		duration := result.Finished.Sub(result.Started)
		result.Duration = duration.String()
		result.DurationMS = DurationMillis(duration)
		result.Summary = summarizeMountedReads(result.Measurements)
		result.Pass = len(result.Steps) > 0
		for _, step := range result.Steps {
			if !step.OK {
				result.Pass = false
				break
			}
		}
	}()

	basePath := fsTestBasePath(fs, mount)
	dir := basePath + "/__qrypt_read_test_" + randomSuffix(6)
	file := dir + "/data.bin"
	result.VirtualPath = file
	mountedFile, err := mountedTestPath(req.MountPoint, file)
	if optionErr != nil {
		err = optionErr
	}
	step := fsStep("validate")
	step.Input = map[string]any{"mount_point": req.MountPoint, "size": size, "block_size": blockSize, "cache_mode": cacheMode, "samples": samples}
	step.finish(time.Now(), err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		return result
	}

	expectedHash, err := createMountedReadFixture(ctx, fs, file, dir, mount, size)
	step = fsStep("prepare")
	step.Input = map[string]any{"path": file, "bytes": size}
	step.finish(result.Started, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		cleanupMountedReadFixture(ctx, fs, file, dir, mount)
		return result
	}
	cleaned := false
	defer func() {
		if !cleaned {
			_ = cleanupMountedReadFixture(context.WithoutCancel(ctx), fs, file, dir, mount)
		}
	}()

	for sample := 1; sample <= samples; sample++ {
		if cacheMode == "cold" || cacheMode == "both" {
			if err := clearMountedReadCache(fs, mount); err != nil {
				result.Steps = append(result.Steps, failedFSStep("clear_read_cache", err))
				return result
			}
			measurement, measurementStarted, readErr := measureMountedFile(ctx, fs, mount, mountedFile, expectedHash, blockSize, "cold", sample)
			events, truncated := mountedReadEvents(fs, mount, file, measurementStarted)
			measurement.VFSReadCalls = mountedVFSReadCalls(events)
			measurement.TraversedVFS = measurement.VFSReadCalls > 0
			result.Metrics = append(result.Metrics, events...)
			result.MetricsTruncated = result.MetricsTruncated || truncated
			if readErr == nil && !measurement.TraversedVFS {
				readErr = fmt.Errorf("mounted read did not traverse qrypt; verify --mount-point")
			}
			result.Measurements = append(result.Measurements, measurement)
			result.Steps = append(result.Steps, mountedReadStep(measurement, readErr))
			if readErr != nil {
				return result
			}
		}
		if cacheMode == "warm" || cacheMode == "both" {
			if cacheMode == "warm" {
				_, primeStarted, primeErr := measureMountedFile(ctx, fs, mount, mountedFile, expectedHash, blockSize, "prime", sample)
				primeEvents, _ := mountedReadEvents(fs, mount, file, primeStarted)
				if primeErr == nil && mountedVFSReadCalls(primeEvents) == 0 {
					primeErr = fmt.Errorf("mounted read did not traverse qrypt; verify --mount-point")
				}
				if primeErr != nil {
					result.Steps = append(result.Steps, failedFSStep("prime_read", primeErr))
					return result
				}
			}
			measurement, measurementStarted, readErr := measureMountedFile(ctx, fs, mount, mountedFile, expectedHash, blockSize, "warm", sample)
			events, truncated := mountedReadEvents(fs, mount, file, measurementStarted)
			measurement.VFSReadCalls = mountedVFSReadCalls(events)
			measurement.TraversedVFS = measurement.VFSReadCalls > 0
			result.Metrics = append(result.Metrics, events...)
			result.MetricsTruncated = result.MetricsTruncated || truncated
			if readErr == nil && !measurement.TraversedVFS {
				readErr = fmt.Errorf("mounted read did not traverse qrypt; verify --mount-point")
			}
			result.Measurements = append(result.Measurements, measurement)
			result.Steps = append(result.Steps, mountedReadStep(measurement, readErr))
			if readErr != nil {
				return result
			}
		}
	}
	cleanupStarted := time.Now()
	cleanupErr := cleanupMountedReadFixture(context.WithoutCancel(ctx), fs, file, dir, mount)
	step = fsStep("cleanup")
	step.finish(cleanupStarted, cleanupErr)
	result.Steps = append(result.Steps, step)
	cleaned = cleanupErr == nil
	return result
}

func mountedTestPath(mountPoint, virtualPath string) (string, error) {
	if strings.TrimSpace(mountPoint) == "" {
		return "", fmt.Errorf("read test requires --mount-point")
	}
	abs, err := filepath.Abs(mountPoint)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat mount point: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("mount point is not a directory: %s", abs)
	}
	return filepath.Join(abs, filepath.FromSlash(strings.TrimPrefix(virtualPath, "/"))), nil
}

func createMountedReadFixture(ctx context.Context, fs vfs.FileSystem, file, dir, mount string, size int64) (string, error) {
	if _, err := fs.Mkdir(ctx, dir); err != nil {
		return "", err
	}
	if err := fs.Create(ctx, file); err != nil {
		return "", err
	}
	hash := sha256.New()
	buf := make([]byte, DefaultMountedReadTestBlockSize)
	for offset := int64(0); offset < size; {
		n := int64(len(buf))
		if remaining := size - offset; remaining < n {
			n = remaining
		}
		fillMountedReadPattern(buf[:n], offset)
		if _, err := fs.WriteAt(ctx, file, buf[:n], offset); err != nil {
			return "", err
		}
		_, _ = hash.Write(buf[:n])
		offset += n
	}
	if err := fs.Flush(ctx, file); err != nil {
		return "", err
	}
	if err := waitVFSSmokeIdle(ctx, fs, mount, 30*time.Minute, nil); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fillMountedReadPattern(buf []byte, offset int64) {
	for i := range buf {
		buf[i] = byte((offset+int64(i))*31 + 17)
	}
}

func clearMountedReadCache(fs vfs.FileSystem, mount string) error {
	if cleaner, ok := fs.(vfs.MountReadCacheCleaner); ok {
		return cleaner.ClearReadCacheForMount(mount)
	}
	if flusher, ok := fs.(vfs.ReadCacheFlusher); ok {
		if err := flusher.FlushReadCache(); err != nil {
			return err
		}
	}
	if cleaner, ok := fs.(vfs.ReadCacheCleaner); ok {
		return cleaner.ClearReadCache()
	}
	return nil
}

func measureMountedFile(ctx context.Context, fs vfs.FileSystem, mount, path, expectedHash string, blockSize int64, mode string, sample int) (MountedReadMeasurement, time.Time, error) {
	measurement := MountedReadMeasurement{Mode: mode, Sample: sample}
	before := fsMountState(fs, mount)
	started := time.Now()
	file, err := os.Open(path)
	measurement.OpenMicros = elapsedMicros(started)
	if err != nil {
		return measurement, started, err
	}
	defer file.Close()
	measurement.OSCacheControl = prepareColdMountedRead(file)

	buf := make([]byte, int(blockSize))
	hash := sha256.New()
	var firstByteAt time.Time
	var firstReadBytes int64
	var callLatencies []int64
	windowStarted := time.Now()
	var windowBytes int64
	for {
		select {
		case <-ctx.Done():
			return measurement, started, ctx.Err()
		default:
		}
		callStarted := time.Now()
		n, readErr := file.Read(buf)
		if n > 0 {
			callLatencies = append(callLatencies, elapsedMicros(callStarted))
			if firstByteAt.IsZero() {
				firstByteAt = time.Now()
				firstReadBytes = int64(n)
				measurement.TTFBMicros = firstByteAt.Sub(started).Microseconds()
			}
			measurement.Bytes += int64(n)
			measurement.ReadCalls++
			windowBytes += int64(n)
			_, _ = hash.Write(buf[:n])
			if elapsed := time.Since(windowStarted); elapsed >= time.Second {
				bps := bytesPerSecond(windowBytes, elapsed)
				if bps > measurement.PeakWindowBPS {
					measurement.PeakWindowBPS = bps
				}
				windowStarted = time.Now()
				windowBytes = 0
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return measurement, started, readErr
		}
	}
	finished := time.Now()
	measurement.DurationMicros = finished.Sub(started).Microseconds()
	measurement.EndToEndBPS = bytesPerSecond(measurement.Bytes, finished.Sub(started))
	if !firstByteAt.IsZero() && measurement.Bytes > firstReadBytes {
		measurement.SteadyBPS = bytesPerSecond(measurement.Bytes-firstReadBytes, finished.Sub(firstByteAt))
	} else {
		measurement.SteadyBPS = measurement.EndToEndBPS
	}
	measurement.ReadLatency = mountedReadLatency(callLatencies)
	measurement.SHA256 = hex.EncodeToString(hash.Sum(nil))
	measurement.ContentMatch = measurement.SHA256 == expectedHash
	after := fsMountState(fs, mount)
	measurement.CacheHits = after.CacheHits - before.CacheHits
	measurement.CacheMisses = after.CacheMisses - before.CacheMisses
	if !measurement.ContentMatch {
		return measurement, started, fmt.Errorf("mounted read content hash mismatch")
	}
	return measurement, started, nil
}

func mountedReadLatency(values []int64) MountedReadLatency {
	if len(values) == 0 {
		return MountedReadLatency{}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return MountedReadLatency{P50: percentileInt64(values, 50), P95: percentileInt64(values, 95), P99: percentileInt64(values, 99), Max: values[len(values)-1]}
}

func summarizeMountedReads(items []MountedReadMeasurement) MountedReadSummary {
	var summary MountedReadSummary
	for _, mode := range []string{"cold", "warm"} {
		var ttfb, speeds []int64
		var peak int64
		for _, item := range items {
			if item.Mode != mode {
				continue
			}
			ttfb = append(ttfb, item.TTFBMicros)
			speeds = append(speeds, item.EndToEndBPS)
			if item.PeakWindowBPS > peak {
				peak = item.PeakWindowBPS
			}
		}
		if len(speeds) == 0 {
			continue
		}
		sort.Slice(ttfb, func(i, j int) bool { return ttfb[i] < ttfb[j] })
		sort.Slice(speeds, func(i, j int) bool { return speeds[i] < speeds[j] })
		modeSummary := &MountedReadModeSummary{Samples: len(speeds), MedianTTFBMicros: percentileInt64(ttfb, 50), P95TTFBMicros: percentileInt64(ttfb, 95), MedianBPS: percentileInt64(speeds, 50), MaxBPS: speeds[len(speeds)-1], PeakWindowBPS: peak}
		if mode == "cold" {
			summary.Cold = modeSummary
		} else {
			summary.Warm = modeSummary
		}
	}
	return summary
}

func percentileInt64(sorted []int64, percentile int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	index := (len(sorted)*percentile + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(sorted) {
		index = len(sorted)
	}
	return sorted[index-1]
}

func elapsedMicros(started time.Time) int64 {
	d := time.Since(started)
	if d <= 0 {
		return 0
	}
	return d.Microseconds()
}

func bytesPerSecond(bytes int64, duration time.Duration) int64 {
	if bytes <= 0 || duration <= 0 {
		return 0
	}
	return int64(float64(bytes) / duration.Seconds())
}

func mountedReadStep(measurement MountedReadMeasurement, err error) FSTestStep {
	step := fsStep(measurement.Mode + "_read")
	step.Input = map[string]any{"sample": measurement.Sample}
	step.Actual = map[string]any{"bytes": measurement.Bytes, "ttfb_us": measurement.TTFBMicros, "end_to_end_bps": measurement.EndToEndBPS, "peak_1s_bps": measurement.PeakWindowBPS, "vfs_read_calls": measurement.VFSReadCalls, "traversed_vfs": measurement.TraversedVFS, "content_match": measurement.ContentMatch}
	step.finish(time.Now().Add(-time.Duration(measurement.DurationMicros)*time.Microsecond), err)
	return step
}

func failedFSStep(operation string, err error) FSTestStep {
	step := fsStep(operation)
	step.finish(time.Now(), err)
	return step
}

func cleanupMountedReadFixture(ctx context.Context, fs vfs.FileSystem, file, dir, mount string) error {
	if err := fs.Remove(ctx, file); err != nil {
		return err
	}
	if err := fs.RemoveDir(ctx, dir); err != nil {
		return err
	}
	return waitVFSSmokeIdle(ctx, fs, mount, 2*time.Minute, nil)
}

func mountedReadEvents(fs vfs.FileSystem, mount, path string, since time.Time) ([]drive.MetricEvent, bool) {
	snapshotter, ok := fs.(vfsDebugSnapshotter)
	if !ok {
		return nil, false
	}
	for _, state := range snapshotter.DebugSnapshot().Mounts {
		if debugMountNameMatches(state.Identity.Name, mount) {
			var events []drive.MetricEvent
			opIDs := map[string]bool{}
			readEvents := state.ReadEvents()
			for _, event := range readEvents {
				if event.Path == "" || event.StartedAt.Before(since) || (event.Path != path && !strings.HasSuffix(path, event.Path)) {
					continue
				}
				events = append(events, event)
				opIDs[event.OpID] = event.OpID != ""
				opIDs[event.ParentOpID] = event.ParentOpID != ""
			}
			for _, event := range state.DriverMetricEvents() {
				if event.At.Before(since) || !opIDs[event.OpID] {
					continue
				}
				events = append(events, event)
			}
			sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
			truncated := len(readEvents) == 1000 && readEvents[0].StartedAt.After(since)
			return events, truncated
		}
	}
	return nil, false
}

func mountedVFSReadCalls(events []drive.MetricEvent) int {
	var calls int
	for _, event := range events {
		if event.Kind == "vfs_read" && event.Phase == "read" && event.Bytes > 0 {
			calls++
		}
	}
	return calls
}
