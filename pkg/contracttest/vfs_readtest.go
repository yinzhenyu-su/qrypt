package contracttest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	vfsread "github.com/yinzhenyu/qrypt/pkg/vfs/read"
)

const (
	DefaultMountedReadTestSize       int64 = 256 << 20
	DefaultMountedReadTestBlockSize  int64 = 1 << 20
	MaxMountedReadTestSize           int64 = 2 << 30
	MaxMountedReadTestSamples              = 10
	DefaultMountedSeekSize           int64 = 1 << 20
	DefaultMountedSeekCount                = 10
	MaxMountedSeekCount                    = 32
	DefaultMountedSeekWarmupChunks         = 2
	MaxMountedSeekWarmupChunks             = 16
	DefaultMountedSeekOverlapTimeout       = 2 * time.Second
)

type MountedReadTestResult struct {
	OpID             string                   `json:"op_id"`
	Mount            string                   `json:"mount"`
	Pass             bool                     `json:"pass"`
	Measurements     []MountedReadMeasurement `json:"measurements"`
	Summary          MountedReadSummary       `json:"summary"`
	SeekMeasurements []MountedSeekMeasurement `json:"seek_measurements,omitempty"`
	SeekSummary      *MountedSeekSummary      `json:"seek_summary,omitempty"`
	Steps            []FSTestStep             `json:"steps"`
	CleanupFailed    bool                     `json:"cleanup_failed,omitempty"`
	Metrics          []drive.MetricEvent      `json:"metrics,omitempty"`
	MetricsTruncated bool                     `json:"metrics_truncated,omitempty"`
	Started          time.Time                `json:"started_at"`
	Finished         time.Time                `json:"finished_at"`
	Duration         string                   `json:"duration"`
	DurationMS       int64                    `json:"duration_ms"`
	RetryCommand     string                   `json:"retry_command,omitempty"`
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
	CacheHitRate   float64            `json:"cache_hit_rate"`
	VFSReadCalls   int                `json:"vfs_read_calls"`
	TraversedVFS   bool               `json:"traversed_vfs"`
	OSCacheControl string             `json:"os_cache_control"`
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

type MountedSeekMeasurement struct {
	Mode              string  `json:"mode"`
	Scenario          string  `json:"scenario"`
	Sample            int     `json:"sample"`
	Index             int     `json:"index"`
	Offset            int64   `json:"offset"`
	Bytes             int64   `json:"bytes"`
	ChunksTouched     int     `json:"chunks_touched"`
	OpenMicros        int64   `json:"open_us"`
	SeekMicros        int64   `json:"seek_us"`
	LoadMicros        int64   `json:"seek_load_us"`
	TotalMicros       int64   `json:"seek_total_us"`
	LoadBPS           int64   `json:"load_bps"`
	CacheHits         int64   `json:"cache_hits"`
	CacheMisses       int64   `json:"cache_misses"`
	CacheHitRate      float64 `json:"cache_hit_rate"`
	VFSReadCalls      int     `json:"vfs_read_calls"`
	VFSOffsetMatch    bool    `json:"vfs_offset_match"`
	TraversedVFS      bool    `json:"traversed_vfs"`
	OSCacheControl    string  `json:"os_cache_control"`
	Overlap           bool    `json:"overlap"`
	OverlapWaitMicros int64   `json:"overlap_wait_us,omitempty"`
	ActiveRequests    int     `json:"active_requests,omitempty"`
	ActiveKind        string  `json:"active_kind,omitempty"`
	ActivePhase       string  `json:"active_phase,omitempty"`
	ActiveOffset      int64   `json:"active_offset,omitempty"`
	ActiveRequested   int64   `json:"active_requested,omitempty"`
	WarmupOffset      int64   `json:"warmup_offset,omitempty"`
	WarmupChunks      int     `json:"warmup_chunks,omitempty"`
}

type MountedSeekModeSummary struct {
	Samples           int   `json:"samples"`
	MedianSeekMicros  int64 `json:"median_seek_us"`
	P95SeekMicros     int64 `json:"p95_seek_us"`
	MedianLoadMicros  int64 `json:"median_seek_load_us"`
	P95LoadMicros     int64 `json:"p95_seek_load_us"`
	P99LoadMicros     int64 `json:"p99_seek_load_us"`
	MaxLoadMicros     int64 `json:"max_seek_load_us"`
	MedianTotalMicros int64 `json:"median_seek_total_us"`
	P95TotalMicros    int64 `json:"p95_seek_total_us"`
	MedianLoadBPS     int64 `json:"median_load_bps"`
	MaxLoadBPS        int64 `json:"max_load_bps"`
}

type MountedSeekSummary struct {
	Cold *MountedSeekModeSummary `json:"cold,omitempty"`
	Warm *MountedSeekModeSummary `json:"warm,omitempty"`
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
	seekSize := ParseXferSize(req.SeekSize)
	if req.SeekSize != "" && (seekSize < 1 || seekSize > 16<<20) {
		optionErr = fmt.Errorf("read test seek size must be between 1 and %d bytes", 16<<20)
	}
	if seekSize <= 0 {
		seekSize = DefaultMountedSeekSize
	}
	if seekSize > size {
		if req.SeekSize != "" {
			optionErr = fmt.Errorf("read test seek size must not exceed file size")
		} else {
			seekSize = size
		}
	}
	samples := req.Samples
	if samples <= 0 {
		samples = 1
	}
	if samples > MaxMountedReadTestSamples {
		optionErr = fmt.Errorf("read test samples must be between 1 and %d", MaxMountedReadTestSamples)
	}
	seekCount := req.SeekCount
	if seekCount <= 0 {
		seekCount = DefaultMountedSeekCount
	}
	if seekCount > MaxMountedSeekCount {
		optionErr = fmt.Errorf("read test seek count must be between 1 and %d", MaxMountedSeekCount)
	}
	seekWarmup := req.SeekWarmup
	if seekWarmup <= 0 {
		seekWarmup = DefaultMountedSeekWarmupChunks
	}
	if seekWarmup > MaxMountedSeekWarmupChunks {
		optionErr = fmt.Errorf("read test seek warmup chunks must be between 1 and %d", MaxMountedSeekWarmupChunks)
	}
	seekOverlapTimeout := DefaultMountedSeekOverlapTimeout
	if req.SeekOverlapTimeout != "" {
		parsed, err := time.ParseDuration(req.SeekOverlapTimeout)
		if err != nil || parsed < 100*time.Millisecond || parsed > 30*time.Second {
			optionErr = fmt.Errorf("read test seek overlap timeout must be between 100ms and 30s")
		} else {
			seekOverlapTimeout = parsed
		}
	}
	cacheMode := strings.ToLower(strings.TrimSpace(req.CacheMode))
	if cacheMode == "" {
		cacheMode = "both"
	}
	if cacheMode != "cold" && cacheMode != "warm" && cacheMode != "both" {
		optionErr = fmt.Errorf("read test cache mode must be cold, warm, or both")
	}
	readPattern := strings.ToLower(strings.TrimSpace(req.ReadPattern))
	if readPattern == "" {
		readPattern = "sequential"
	}
	if readPattern != "sequential" && readPattern != "seek" && readPattern != "both" {
		optionErr = fmt.Errorf("read test pattern must be sequential, seek, or both")
	}
	seekScenario := strings.ToLower(strings.TrimSpace(req.SeekScenario))
	if seekScenario == "" {
		seekScenario = "isolated"
	}
	if seekScenario != "isolated" && seekScenario != "prefetch" && seekScenario != "concurrent" {
		optionErr = fmt.Errorf("read test seek scenario must be isolated, prefetch, or concurrent")
	}
	if seekScenario != "isolated" {
		minimumSize := int64(2*(seekWarmup+vfsread.PrefetchLimit*vfsread.SequentialPrefetchChunks))*vfsread.ChunkSize + seekSize
		if size < minimumSize {
			optionErr = fmt.Errorf("read test file size must be at least %d bytes for %s seek scenario", minimumSize, seekScenario)
		}
	}
	result := &MountedReadTestResult{
		OpID: newDebugOperationID("read"), Mount: mount,
		Started: time.Now(), Steps: make([]FSTestStep, 0, 4+samples*(2+seekCount*2)),
		RetryCommand: fmt.Sprintf("qrypt debug test read --mount %s --mount-point PATH --size %d --block-size %d --cache-mode %s --pattern %s --samples %d --seek-count %d --seek-size %d --seek-scenario %s --seek-warmup-chunks %d --seek-overlap-timeout %s --socket PATH", mount, size, blockSize, cacheMode, readPattern, samples, seekCount, seekSize, seekScenario, seekWarmup, seekOverlapTimeout),
	}
	defer func() {
		result.Finished = time.Now()
		duration := result.Finished.Sub(result.Started)
		result.Duration = duration.String()
		result.DurationMS = DurationMillis(duration)
		result.Summary = summarizeMountedReads(result.Measurements)
		result.SeekSummary = summarizeMountedSeeks(result.SeekMeasurements)
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
	mountedFile, err := mountedTestPath(req.MountPoint, file)
	if optionErr != nil {
		err = optionErr
	}
	step := fsStep("validate")
	step.Input = map[string]any{"mount_point": req.MountPoint, "size": size, "block_size": blockSize, "cache_mode": cacheMode, "pattern": readPattern, "samples": samples, "seek_count": seekCount, "seek_size": seekSize, "seek_scenario": seekScenario, "seek_warmup_chunks": seekWarmup, "seek_overlap_timeout": seekOverlapTimeout.String()}
	step.finish(time.Now(), err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		return result
	}

	defer func() {
		cleanupStarted := time.Now()
		cleanupErr := cleanupMountedReadFixture(context.WithoutCancel(ctx), fs, file, dir, mount)
		step := fsStep("cleanup")
		step.finish(cleanupStarted, cleanupErr)
		result.Steps = append(result.Steps, step)
		result.CleanupFailed = cleanupErr != nil
	}()

	prepareStarted := time.Now()
	expectedHash, err := createMountedReadFixture(ctx, fs, file, dir, mount, size)
	step = fsStep("prepare")
	step.Input = map[string]any{"path": file, "bytes": size}
	step.finish(prepareStarted, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		return result
	}

	if readPattern == "sequential" || readPattern == "both" {
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
	}

	if readPattern == "seek" || readPattern == "both" {
		if err := waitMountedReadIdle(ctx, fs, mount, file, seekOverlapTimeout, seekScenario != "isolated"); err != nil {
			result.Steps = append(result.Steps, failedFSStep("wait_read_idle", err))
			return result
		}
		offsets := mountedSeekOffsets(size, seekSize, seekCount)
		for sample := 1; sample <= samples; sample++ {
			for index, offset := range offsets {
				coldWarmup, warmWarmup := mountedSeekWarmupOffsets(size, offset, seekSize, seekWarmup)
				if cacheMode == "cold" || cacheMode == "both" {
					if err := clearMountedReadCache(fs, mount); err != nil {
						result.Steps = append(result.Steps, failedFSStep("clear_read_cache", err))
						return result
					}
					measurement, events, truncated, seekErr := runMountedSeekProbe(ctx, fs, mount, mountedFile, file, offset, seekSize, coldWarmup, seekScenario, seekWarmup, seekOverlapTimeout, "cold", sample, index+1)
					result.SeekMeasurements = append(result.SeekMeasurements, measurement)
					result.Metrics = append(result.Metrics, events...)
					result.MetricsTruncated = result.MetricsTruncated || truncated
					result.Steps = append(result.Steps, mountedSeekStep(measurement, seekErr))
					if seekErr != nil {
						return result
					}
				}
				if cacheMode == "warm" || cacheMode == "both" {
					if cacheMode == "warm" {
						prime, _, _, primeErr := runMountedSeekProbe(ctx, fs, mount, mountedFile, file, offset, seekSize, 0, "isolated", seekWarmup, seekOverlapTimeout, "prime", sample, index+1)
						if primeErr != nil {
							result.Steps = append(result.Steps, mountedSeekStep(prime, primeErr))
							return result
						}
					}
					measurement, events, truncated, seekErr := runMountedSeekProbe(ctx, fs, mount, mountedFile, file, offset, seekSize, warmWarmup, seekScenario, seekWarmup, seekOverlapTimeout, "warm", sample, index+1)
					result.SeekMeasurements = append(result.SeekMeasurements, measurement)
					result.Metrics = append(result.Metrics, events...)
					result.MetricsTruncated = result.MetricsTruncated || truncated
					result.Steps = append(result.Steps, mountedSeekStep(measurement, seekErr))
					if seekErr != nil {
						return result
					}
				}
			}
		}
	}
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
	if windowBytes > 0 {
		if bps := bytesPerSecond(windowBytes, finished.Sub(windowStarted)); bps > measurement.PeakWindowBPS {
			measurement.PeakWindowBPS = bps
		}
	}
	measurement.DurationMicros = finished.Sub(started).Microseconds()
	measurement.EndToEndBPS = bytesPerSecond(measurement.Bytes, finished.Sub(started))
	if !firstByteAt.IsZero() && measurement.Bytes > firstReadBytes {
		measurement.SteadyBPS = bytesPerSecond(measurement.Bytes-firstReadBytes, finished.Sub(firstByteAt))
	} else {
		measurement.SteadyBPS = measurement.EndToEndBPS
	}
	measurement.ReadLatency = mountedReadLatency(callLatencies)
	after := fsMountState(fs, mount)
	measurement.CacheHits = after.CacheHits - before.CacheHits
	measurement.CacheMisses = after.CacheMisses - before.CacheMisses
	totalCacheLookups := measurement.CacheHits + measurement.CacheMisses
	if totalCacheLookups > 0 {
		measurement.CacheHitRate = float64(measurement.CacheHits) / float64(totalCacheLookups)
	}
	gotHash := hex.EncodeToString(hash.Sum(nil))
	if gotHash != expectedHash {
		return measurement, started, fmt.Errorf("mounted read content hash mismatch")
	}
	return measurement, started, nil
}

func mountedSeekOffsets(fileSize, probeSize int64, count int) []int64 {
	if fileSize <= 0 || probeSize <= 0 || count <= 0 {
		return nil
	}
	if probeSize > fileSize {
		probeSize = fileSize
	}
	maxOffset := fileSize - probeSize
	if maxOffset == 0 {
		return []int64{0}
	}
	maxDistinct := int(maxOffset/probeSize) + 1
	if count > maxDistinct {
		count = maxDistinct
	}
	offsets := make([]int64, 0, count)
	for index := 1; index <= count; index++ {
		offset := int64(index) * maxOffset / int64(count)
		if len(offsets) == 0 || offsets[len(offsets)-1] != offset {
			offsets = append(offsets, offset)
		}
	}
	return offsets
}

func mountedSeekWarmupOffsets(fileSize, targetOffset, targetSize int64, chunks int) (int64, int64) {
	loadSpan := int64(chunks+vfsread.PrefetchLimit*vfsread.SequentialPrefetchChunks) * vfsread.ChunkSize
	maxStart := fileSize - loadSpan
	if maxStart <= 0 {
		return 0, 0
	}
	var selected []int64
	for slot := 0; slot <= 32 && len(selected) < 2; slot++ {
		start := int64(slot) * maxStart / 32
		start = start / vfsread.ChunkSize * vfsread.ChunkSize
		if rangesOverlap(start, start+loadSpan, targetOffset, targetOffset+targetSize) {
			continue
		}
		if len(selected) > 0 && rangesOverlap(start, start+loadSpan, selected[0], selected[0]+loadSpan) {
			continue
		}
		selected = append(selected, start)
	}
	if len(selected) == 0 {
		return 0, 0
	}
	if len(selected) == 1 {
		return selected[0], selected[0]
	}
	return selected[0], selected[1]
}

func rangesOverlap(startA, endA, startB, endB int64) bool {
	return startA < endB && startB < endA
}

func runMountedSeekProbe(ctx context.Context, fs vfs.FileSystem, mount, mountedPath, virtualPath string, offset, size, warmupOffset int64, scenario string, warmupChunks int, overlapTimeout time.Duration, mode string, sample, index int) (MountedSeekMeasurement, []drive.MetricEvent, bool, error) {
	measurement, started, err := measureMountedSeekScenario(ctx, fs, mount, mountedPath, virtualPath, offset, size, warmupOffset, scenario, warmupChunks, overlapTimeout, mode, sample, index)
	idleErr := waitMountedReadIdle(ctx, fs, mount, virtualPath, overlapTimeout, scenario != "isolated")
	err = errors.Join(err, idleErr)
	events, truncated := mountedReadEvents(fs, mount, virtualPath, started)
	measurement.VFSReadCalls = mountedVFSReadCalls(events)
	measurement.VFSOffsetMatch = mountedVFSReadAtOffset(events, offset)
	measurement.TraversedVFS = measurement.VFSOffsetMatch
	if err == nil && !measurement.TraversedVFS {
		err = fmt.Errorf("mounted seek read did not traverse qrypt; verify --mount-point")
	}
	return measurement, events, truncated, err
}

func measureMountedSeek(ctx context.Context, fs vfs.FileSystem, mount, path string, offset, size int64, mode string, sample, index int) (MountedSeekMeasurement, time.Time, error) {
	return measureMountedSeekScenario(ctx, fs, mount, path, "", offset, size, 0, "isolated", 0, 0, mode, sample, index)
}

func measureMountedSeekScenario(ctx context.Context, fs vfs.FileSystem, mount, mountedPath, virtualPath string, offset, size, warmupOffset int64, scenario string, warmupChunks int, overlapTimeout time.Duration, mode string, sample, index int) (MountedSeekMeasurement, time.Time, error) {
	measurement := MountedSeekMeasurement{
		Mode: mode, Scenario: scenario, Sample: sample, Index: index, Offset: offset,
		ChunksTouched: mountedSeekChunksTouched(offset, size),
	}
	started := time.Now()
	file, err := os.Open(mountedPath)
	measurement.OpenMicros = elapsedMicros(started)
	if err != nil {
		return measurement, started, err
	}
	defer file.Close()
	measurement.OSCacheControl = prepareColdMountedRead(file)

	switch scenario {
	case "isolated":
		before := fsMountState(fs, mount)
		err = measureMountedSeekOnFile(ctx, fs, mount, file, offset, size, before, &measurement)
		return measurement, started, err
	case "prefetch":
		measurement.WarmupOffset = warmupOffset
		measurement.WarmupChunks = warmupChunks
		if err := readMountedWarmup(file, warmupOffset, warmupChunks); err != nil {
			return measurement, started, fmt.Errorf("seek prefetch warmup: %w", err)
		}
		overlap, err := waitMountedSeekOverlap(ctx, fs, mount, virtualPath, scenario, overlapTimeout)
		applyMountedSeekOverlap(&measurement, overlap)
		if err != nil {
			return measurement, started, err
		}
		before := fsMountState(fs, mount)
		err = measureMountedSeekOnFile(ctx, fs, mount, file, offset, size, before, &measurement)
		return measurement, started, err
	case "concurrent":
		measurement.WarmupOffset = warmupOffset
		measurement.WarmupChunks = warmupChunks
		background, err := os.Open(mountedPath)
		if err != nil {
			return measurement, started, fmt.Errorf("open concurrent reader: %w", err)
		}
		measurement.OSCacheControl += ",background=" + prepareColdMountedRead(background)
		stop := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- readMountedConcurrent(background, warmupOffset, warmupChunks, stop)
		}()
		overlap, overlapErr := waitMountedSeekOverlap(ctx, fs, mount, virtualPath, scenario, overlapTimeout)
		applyMountedSeekOverlap(&measurement, overlap)
		if overlapErr == nil {
			before := fsMountState(fs, mount)
			err = measureMountedSeekOnFile(ctx, fs, mount, file, offset, size, before, &measurement)
		}
		close(stop)
		_ = background.Close()
		select {
		case <-done:
		case <-time.After(overlapTimeout):
			if err == nil {
				err = fmt.Errorf("timeout stopping concurrent sequential reader after %s", overlapTimeout)
			}
		}
		if overlapErr != nil {
			return measurement, started, overlapErr
		}
		return measurement, started, err
	default:
		return measurement, started, fmt.Errorf("unknown mounted seek scenario %q", scenario)
	}
}

func measureMountedSeekOnFile(ctx context.Context, fs vfs.FileSystem, mount string, file *os.File, offset, size int64, before *FSMountState, measurement *MountedSeekMeasurement) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	seekStarted := time.Now()
	actualOffset, err := file.Seek(offset, io.SeekStart)
	seekFinished := time.Now()
	measurement.SeekMicros = seekFinished.Sub(seekStarted).Microseconds()
	if err != nil {
		return err
	}
	if actualOffset != offset {
		return fmt.Errorf("mounted seek returned offset %d, want %d", actualOffset, offset)
	}

	buf := make([]byte, int(size))
	loadStarted := time.Now()
	n, readErr := io.ReadFull(file, buf)
	loadFinished := time.Now()
	loadDuration := loadFinished.Sub(loadStarted)
	measurement.Bytes = int64(n)
	measurement.LoadMicros = loadDuration.Microseconds()
	measurement.TotalMicros = loadFinished.Sub(seekStarted).Microseconds()
	measurement.LoadBPS = bytesPerSecond(int64(n), loadDuration)
	after := fsMountState(fs, mount)
	measurement.CacheHits = after.CacheHits - before.CacheHits
	measurement.CacheMisses = after.CacheMisses - before.CacheMisses
	if lookups := measurement.CacheHits + measurement.CacheMisses; lookups > 0 {
		measurement.CacheHitRate = float64(measurement.CacheHits) / float64(lookups)
	}
	if readErr != nil {
		return readErr
	}
	for i, value := range buf {
		want := byte((offset+int64(i))*31 + 17)
		if value != want {
			return fmt.Errorf("mounted seek content mismatch at offset %d", offset+int64(i))
		}
	}
	return nil
}

func readMountedWarmup(file *os.File, offset int64, chunks int) error {
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, vfsread.ChunkSize)
	for index := 0; index < chunks; index++ {
		if _, err := io.ReadFull(file, buf); err != nil {
			return err
		}
	}
	return nil
}

func readMountedConcurrent(file *os.File, offset int64, chunks int, stop <-chan struct{}) error {
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, vfsread.ChunkSize)
	for index := 0; index < chunks; index++ {
		select {
		case <-stop:
			return nil
		default:
		}
		if _, err := io.ReadFull(file, buf); err != nil {
			select {
			case <-stop:
				return nil
			default:
				return err
			}
		}
	}
	return nil
}

type mountedSeekOverlap struct {
	Found     bool
	Wait      time.Duration
	Count     int
	Kind      string
	Phase     string
	Offset    int64
	Requested int64
}

func mountedSeekChunksTouched(offset, size int64) int {
	if offset < 0 || size <= 0 {
		return 0
	}
	return int((offset%vfsread.ChunkSize + size + vfsread.ChunkSize - 1) / vfsread.ChunkSize)
}

func waitMountedReadIdle(ctx context.Context, fs vfs.FileSystem, mount, path string, timeout time.Duration, required bool) error {
	provider, ok := fs.(diagnostics.DebugActiveProvider)
	if !ok {
		if required {
			return fmt.Errorf("loaded seek scenario requires VFS active-operation diagnostics")
		}
		return nil
	}
	const quietWindow = 10 * time.Millisecond
	var idleSince time.Time
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		mounts, err := provider.DebugActiveOps(ctx, []string{mount})
		if err != nil {
			return fmt.Errorf("inspect active reads: %w", err)
		}
		if hasMountedReadActive(mounts, path) {
			idleSince = time.Time{}
		} else if idleSince.IsZero() {
			idleSince = time.Now()
		} else if time.Since(idleSince) >= quietWindow {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("timeout after %s waiting for mounted reads to become idle", timeout)
		case <-ticker.C:
		}
	}
}

func hasMountedReadActive(mounts []diagnostics.DebugActiveMount, path string) bool {
	for _, mount := range mounts {
		for _, op := range mount.Ops {
			if !mountedReadPathMatches(op.Path, path) {
				continue
			}
			switch op.Kind {
			case "vfs_read", "vfs_window_load", "vfs_prefetch", "vfs_wait":
				return true
			}
		}
	}
	return false
}

func waitMountedSeekOverlap(ctx context.Context, fs vfs.FileSystem, mount, path, scenario string, timeout time.Duration) (mountedSeekOverlap, error) {
	provider, ok := fs.(diagnostics.DebugActiveProvider)
	if !ok {
		return mountedSeekOverlap{}, fmt.Errorf("%s seek scenario requires VFS active-operation diagnostics", scenario)
	}
	started := time.Now()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		mounts, err := provider.DebugActiveOps(ctx, []string{mount})
		if err != nil {
			return mountedSeekOverlap{}, fmt.Errorf("inspect active reads: %w", err)
		}
		if overlap := findMountedSeekOverlap(mounts, path, scenario); overlap.Found {
			overlap.Wait = time.Since(started)
			return overlap, nil
		}
		select {
		case <-ctx.Done():
			return mountedSeekOverlap{Wait: time.Since(started)}, ctx.Err()
		case <-timer.C:
			return mountedSeekOverlap{Wait: time.Since(started)}, fmt.Errorf("timeout after %s waiting for %s seek overlap", timeout, scenario)
		case <-ticker.C:
		}
	}
}

func findMountedSeekOverlap(mounts []diagnostics.DebugActiveMount, path, scenario string) mountedSeekOverlap {
	var result mountedSeekOverlap
	for _, mount := range mounts {
		for _, op := range mount.Ops {
			if op.Phase != "fetch_window" || !mountedReadPathMatches(op.Path, path) {
				continue
			}
			if scenario == "prefetch" && op.Kind != "vfs_prefetch" {
				continue
			}
			if scenario == "concurrent" && op.Kind != "vfs_window_load" && op.Kind != "vfs_prefetch" {
				continue
			}
			result.Count++
			if !result.Found {
				result.Found = true
				result.Kind = op.Kind
				result.Phase = op.Phase
				result.Offset = op.Offset
				result.Requested = op.Requested
			}
		}
	}
	return result
}

func mountedReadPathMatches(actual, requested string) bool {
	if actual == "" || requested == "" {
		return false
	}
	return actual == requested || strings.HasSuffix(requested, actual) || strings.HasSuffix(actual, requested)
}

func applyMountedSeekOverlap(measurement *MountedSeekMeasurement, overlap mountedSeekOverlap) {
	measurement.Overlap = overlap.Found
	measurement.OverlapWaitMicros = overlap.Wait.Microseconds()
	measurement.ActiveRequests = overlap.Count
	measurement.ActiveKind = overlap.Kind
	measurement.ActivePhase = overlap.Phase
	measurement.ActiveOffset = overlap.Offset
	measurement.ActiveRequested = overlap.Requested
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

func summarizeMountedSeeks(items []MountedSeekMeasurement) *MountedSeekSummary {
	if len(items) == 0 {
		return nil
	}
	summary := &MountedSeekSummary{}
	for _, mode := range []string{"cold", "warm"} {
		var seek, load, total, speeds []int64
		for _, item := range items {
			if item.Mode != mode {
				continue
			}
			seek = append(seek, item.SeekMicros)
			load = append(load, item.LoadMicros)
			total = append(total, item.TotalMicros)
			speeds = append(speeds, item.LoadBPS)
		}
		if len(load) == 0 {
			continue
		}
		sort.Slice(seek, func(i, j int) bool { return seek[i] < seek[j] })
		sort.Slice(load, func(i, j int) bool { return load[i] < load[j] })
		sort.Slice(total, func(i, j int) bool { return total[i] < total[j] })
		sort.Slice(speeds, func(i, j int) bool { return speeds[i] < speeds[j] })
		modeSummary := &MountedSeekModeSummary{
			Samples: len(load), MedianSeekMicros: percentileInt64(seek, 50), P95SeekMicros: percentileInt64(seek, 95),
			MedianLoadMicros: percentileInt64(load, 50), P95LoadMicros: percentileInt64(load, 95),
			P99LoadMicros: percentileInt64(load, 99), MaxLoadMicros: load[len(load)-1],
			MedianTotalMicros: percentileInt64(total, 50), P95TotalMicros: percentileInt64(total, 95),
			MedianLoadBPS: percentileInt64(speeds, 50), MaxLoadBPS: speeds[len(speeds)-1],
		}
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
	step.Actual = map[string]any{"bytes": measurement.Bytes, "open_us": measurement.OpenMicros, "ttfb_us": measurement.TTFBMicros, "end_to_end_bps": measurement.EndToEndBPS, "steady_bps": measurement.SteadyBPS, "peak_1s_bps": measurement.PeakWindowBPS, "read_calls": measurement.ReadCalls, "vfs_read_calls": measurement.VFSReadCalls, "traversed_vfs": measurement.TraversedVFS, "cache_hit_rate": measurement.CacheHitRate}
	step.finish(time.Now().Add(-time.Duration(measurement.DurationMicros)*time.Microsecond), err)
	return step
}

func mountedSeekStep(measurement MountedSeekMeasurement, err error) FSTestStep {
	step := fsStep(measurement.Mode + "_seek")
	step.Input = map[string]any{
		"sample": measurement.Sample, "index": measurement.Index, "offset": measurement.Offset,
		"bytes": measurement.Bytes, "chunks_touched": measurement.ChunksTouched, "scenario": measurement.Scenario,
	}
	step.Actual = map[string]any{
		"seek_us": measurement.SeekMicros, "seek_load_us": measurement.LoadMicros,
		"seek_total_us": measurement.TotalMicros, "load_bps": measurement.LoadBPS,
		"vfs_read_calls": measurement.VFSReadCalls, "traversed_vfs": measurement.TraversedVFS,
		"vfs_offset_match": measurement.VFSOffsetMatch, "cache_hit_rate": measurement.CacheHitRate,
		"overlap": measurement.Overlap, "overlap_wait_us": measurement.OverlapWaitMicros,
		"active_requests": measurement.ActiveRequests, "active_kind": measurement.ActiveKind,
		"active_offset": measurement.ActiveOffset, "active_requested": measurement.ActiveRequested,
	}
	step.finish(time.Now().Add(-time.Duration(measurement.TotalMicros)*time.Microsecond), err)
	return step
}

func failedFSStep(operation string, err error) FSTestStep {
	step := fsStep(operation)
	step.finish(time.Now(), err)
	return step
}

func cleanupMountedReadFixture(ctx context.Context, fs vfs.FileSystem, file, dir, mount string) error {
	var errs []error
	if err := retryMountedReadCleanup(ctx, "remove file", func() error {
		return fs.Remove(ctx, file)
	}); err != nil {
		errs = append(errs, err)
	}
	if idleErr := waitVFSSmokeIdle(ctx, fs, mount, 2*time.Minute, nil); idleErr != nil {
		errs = append(errs, fmt.Errorf("wait for file removal: %w", idleErr))
	}
	if err := retryMountedReadCleanup(ctx, "remove directory", func() error {
		return fs.RemoveDir(ctx, dir)
	}); err != nil {
		errs = append(errs, err)
	}
	if idleErr := waitVFSSmokeIdle(ctx, fs, mount, 2*time.Minute, nil); idleErr != nil {
		errs = append(errs, fmt.Errorf("wait for directory removal: %w", idleErr))
	}
	return errors.Join(errs...)
}

func retryMountedReadCleanup(ctx context.Context, operation string, fn func() error) error {
	const (
		maxRetries = 3
		retryDelay = 100 * time.Millisecond
	)
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := fn()
		if err == nil || vfs.IsNotFound(err) || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		lastErr = fmt.Errorf("%s (attempt %d/%d): %w", operation, attempt, maxRetries, err)
		if attempt == maxRetries {
			break
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(lastErr, ctx.Err())
		case <-timer.C:
		}
	}
	return lastErr
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
			truncated := len(readEvents) == vfsread.HistoryLimit && readEvents[0].StartedAt.After(since)
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

func mountedVFSReadAtOffset(events []drive.MetricEvent, offset int64) bool {
	for _, event := range events {
		if event.Kind == "vfs_read" && event.Phase == "read" && event.Bytes > 0 &&
			event.Offset <= offset && offset-event.Offset < event.Bytes {
			return true
		}
	}
	return false
}
