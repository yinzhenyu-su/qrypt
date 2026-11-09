package contracttest

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

type FSTestResult struct {
	OpID            string            `json:"op_id"`
	Mount           string            `json:"mount"`
	Pass            bool              `json:"pass"`
	Steps           []FSTestStep      `json:"steps"`
	PendingTimeline []FSPendingSample `json:"pending_timeline,omitempty"`
	FinalState      *FSMountState     `json:"final_state,omitempty"`
	Started         time.Time         `json:"started_at"`
	Finished        time.Time         `json:"finished_at"`
	Duration        string            `json:"duration"`
	DurationMS      int64             `json:"duration_ms"`
	RetryCommand    string            `json:"retry_command,omitempty"`
}

type FSTestStep struct {
	Operation     string         `json:"operation"`
	OK            bool           `json:"ok"`
	Error         string         `json:"error,omitempty"`
	ErrorCategory string         `json:"error_category,omitempty"`
	Duration      string         `json:"duration"`
	DurationMS    int64          `json:"duration_ms"`
	Input         map[string]any `json:"input,omitempty"`
	Expected      map[string]any `json:"expected,omitempty"`
	Actual        map[string]any `json:"actual,omitempty"`
}

type FSPendingSample struct {
	Attempt            int        `json:"attempt"`
	Elapsed            string     `json:"elapsed"`
	ElapsedMS          int64      `json:"elapsed_ms"`
	PendingCount       int        `json:"pending_count"`
	PermanentFailCount int        `json:"permanent_fail_count,omitempty"`
	UploadCount        int        `json:"upload_count"`
	DeleteTimers       int        `json:"delete_timer_count"`
	ActiveDeletes      int        `json:"active_delete_count,omitempty"`
	Pending            []string   `json:"pending,omitempty"`
	PermanentFail      []string   `json:"permanent_fail,omitempty"`
	Uploads            []FSUpload `json:"uploads,omitempty"`
	DeleteTimerFor     []string   `json:"delete_timers,omitempty"`
	Error              string     `json:"error,omitempty"`
}

type FSMountState struct {
	Mount                 string     `json:"mount"`
	PendingCount          int        `json:"pending_count"`
	PermanentFailCount    int        `json:"permanent_fail_count,omitempty"`
	UploadCount           int        `json:"upload_count"`
	DeleteTimers          int        `json:"delete_timer_count"`
	ActiveDeletes         int        `json:"active_delete_count,omitempty"`
	Pending               []string   `json:"pending,omitempty"`
	PermanentFail         []string   `json:"permanent_fail,omitempty"`
	Uploads               []FSUpload `json:"uploads,omitempty"`
	CacheHits             int64      `json:"cache_hits,omitempty"`
	CacheMisses           int64      `json:"cache_misses,omitempty"`
	CacheHitRatio         float64    `json:"cache_hit_ratio,omitempty"`
	CacheErrors           int        `json:"cache_errors,omitempty"`
	ReadCacheFiles        int        `json:"read_cache_files,omitempty"`
	ReadCacheBytes        int64      `json:"read_cache_bytes,omitempty"`
	StagingOrphans        int        `json:"staging_orphans,omitempty"`
	StagingSizeMismatches int        `json:"staging_size_mismatches,omitempty"`
	WindowLoads           int        `json:"window_loads,omitempty"`
	Prefetches            int        `json:"prefetches,omitempty"`
}

type FSUpload struct {
	Path          string `json:"path"`
	State         string `json:"state"`
	BytesTotal    int64  `json:"bytes_total,omitempty"`
	BytesUploaded int64  `json:"bytes_uploaded,omitempty"`
	RetryCount    int    `json:"retry_count,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
}

func RunVFSSmokeTest(ctx context.Context, fs vfs.FileSystem, mount string, size int64) *FSTestResult {
	if size <= 0 {
		size = 32
	}
	result := &FSTestResult{
		OpID:         newDebugOperationID("fs"),
		Mount:        mount,
		Started:      time.Now(),
		Steps:        make([]FSTestStep, 0, 8),
		RetryCommand: fmt.Sprintf("qrypt probe test fs --mount %s", mount),
	}
	defer func() {
		result.Finished = time.Now()
		duration := result.Finished.Sub(result.Started)
		result.Duration = duration.String()
		result.DurationMS = DurationMillis(duration)
		result.FinalState = fsFinalMountState(ctx, fs, mount)
		result.Pass = true
		for _, step := range result.Steps {
			if !step.OK {
				result.Pass = false
				break
			}
		}
	}()

	basePath := fsTestBasePath(fs, mount)
	dir := basePath + "/__qrypt_fs_test_" + randomSuffix(6)
	file := dir + "/data.txt"
	data := bytes.Repeat([]byte("x"), int(size))

	step := fsStep("mkdir")
	step.Input = map[string]any{"path": dir}
	start := time.Now()
	_, err := fs.Mkdir(ctx, dir)
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		return result
	}
	// Every failure path below must still clean up the remote test directory.
	// Use a context detached from the probe request so a timeout or cancel of
	// the probe does not abort cleanup midway.
	defer func() {
		cleanupStart := time.Now()
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if cleanupErr := cleanupVFSProbeFixture(cleanupCtx, fs, mount, dir, file); cleanupErr != nil {
			cleanupStep := fsStep("cleanup")
			cleanupStep.finish(cleanupStart, cleanupErr)
			result.Steps = append(result.Steps, cleanupStep)
		}
	}()

	step = fsStep("write")
	step.Input = map[string]any{"path": file, "bytes": len(data)}
	step.Expected = map[string]any{"pending_created": true}
	start = time.Now()
	err = fs.Create(ctx, file)
	if err == nil {
		_, err = fs.WriteAt(ctx, file, data, 0)
	}
	step.Actual = map[string]any{"pending_count": pendingCount(fs)}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		return result
	}

	step = fsStep("flush")
	step.Input = map[string]any{"path": file}
	start = time.Now()
	err = fs.Flush(ctx, file)
	step.Actual = map[string]any{"pending_count": pendingCount(fs)}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		return result
	}

	step = fsStep("wait_upload")
	step.Expected = map[string]any{"pending_count": 0, "upload_count": 0}
	start = time.Now()
	err = waitVFSSmokeIdle(ctx, fs, mount, 2*time.Minute, &result.PendingTimeline)
	state := fsMountState(fs, mount)
	if err == nil {
		err = permanentFailureError(state)
	}
	step.Actual = map[string]any{"state": state}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		return result
	}

	step = fsStep("read")
	step.Input = map[string]any{"path": file}
	step.Expected = map[string]any{"bytes": len(data), "content_match": true}
	start = time.Now()
	readData, err := readVFSFile(ctx, fs, file)
	if err == nil && !bytes.Equal(readData, data) {
		err = fmt.Errorf("content mismatch: got %d bytes, want %d", len(readData), len(data))
	}
	step.Actual = map[string]any{"bytes": len(readData), "content_match": err == nil}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)

	step = fsStep("remove")
	step.Input = map[string]any{"file": file, "dir": dir}
	start = time.Now()
	removeErr := cleanupVFSPath(ctx, fs, file, false)
	if removeErr == nil {
		removeErr = cleanupVFSPath(ctx, fs, dir, true)
	}
	step.finish(start, removeErr)
	result.Steps = append(result.Steps, step)
	if removeErr != nil {
		return result
	}

	step = fsStep("wait_cleanup")
	step.Expected = map[string]any{"pending_count": 0, "delete_timer_count": 0}
	start = time.Now()
	err = waitVFSSmokeIdle(ctx, fs, mount, 2*time.Minute, &result.PendingTimeline)
	step.Actual = map[string]any{"state": fsMountState(fs, mount)}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	return result
}

func fsStep(operation string) FSTestStep {
	return FSTestStep{Operation: operation, Duration: "0s"}
}

func (s *FSTestStep) finish(start time.Time, err error) {
	duration := time.Since(start)
	s.Duration = duration.String()
	s.DurationMS = DurationMillis(duration)
	if err != nil {
		s.OK = false
		s.Error = err.Error()
		s.ErrorCategory = drive.ErrorCategory(err)
		return
	}
	s.OK = true
}

func waitVFSSmokeIdle(ctx context.Context, fs any, mount string, timeout time.Duration, timeline *[]FSPendingSample) error {
	start := time.Now()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	attempt := 0
	for {
		attempt++
		sample := fsPendingSample(fs, mount, attempt, time.Since(start))
		if timeline != nil {
			*timeline = append(*timeline, sample)
		}
		// Permanently failed uploads are terminal: they will never become
		// active work again, so counting them as pending would make every
		// idle wait spin until its timeout. Callers that need a health
		// verdict inspect PermanentFailCount separately.
		activePending := sample.PendingCount - sample.PermanentFailCount
		if activePending == 0 && sample.UploadCount == 0 && sample.DeleteTimers == 0 && sample.ActiveDeletes == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("timeout waiting for VFS idle after %s", timeout)
		case <-ticker.C:
		}
	}
}

func fsPendingSample(fs any, mount string, attempt int, elapsed time.Duration) FSPendingSample {
	state := fsMountState(fs, mount)
	sample := FSPendingSample{
		Attempt:            attempt,
		Elapsed:            elapsed.String(),
		ElapsedMS:          DurationMillis(elapsed),
		PendingCount:       state.PendingCount,
		PermanentFailCount: state.PermanentFailCount,
		UploadCount:        state.UploadCount,
		DeleteTimers:       state.DeleteTimers,
		ActiveDeletes:      state.ActiveDeletes,
		Pending:            state.Pending,
		PermanentFail:      state.PermanentFail,
		Uploads:            state.Uploads,
	}
	if snapshotter, ok := fs.(vfsDebugSnapshotter); ok {
		snapshot := snapshotter.DebugSnapshot()
		for _, mountState := range snapshot.Mounts {
			if !debugMountNameMatches(mountState.Identity.Name, mount) {
				continue
			}
			for _, timer := range mountState.ActiveDeleteTimers() {
				sample.DeleteTimerFor = append(sample.DeleteTimerFor, timer.Path)
			}
		}
	}
	return sample
}

func fsMountState(fs any, mount string) *FSMountState {
	state := &FSMountState{Mount: mount}
	if snapshotter, ok := fs.(vfsDebugSnapshotter); ok {
		snapshot := snapshotter.DebugSnapshot()
		for _, mountState := range snapshot.Mounts {
			if !debugMountNameMatches(mountState.Identity.Name, mount) {
				continue
			}
			state.PendingCount = len(mountState.PendingUploads())
			state.UploadCount = len(mountState.ActiveUploads())
			state.DeleteTimers = len(mountState.ActiveDeleteTimers())
			state.ActiveDeletes = len(mountState.ActiveDeletes())
			cache := mountState.ReadCacheState()
			state.CacheHits = cache.Hits
			state.CacheMisses = cache.Misses
			if total := cache.Hits + cache.Misses; total > 0 {
				state.CacheHitRatio = float64(cache.Hits) / float64(total)
			}
			if cache.LastGetError != "" {
				state.CacheErrors++
			}
			if cache.LastPutError != "" {
				state.CacheErrors++
			}
			state.ReadCacheFiles = cache.FileCount
			state.ReadCacheBytes = cache.Bytes
			state.WindowLoads = mountState.Runtime.WindowLoads
			state.Prefetches = mountState.Runtime.Prefetches
			for _, pending := range mountState.PendingUploads() {
				state.Pending = append(state.Pending, pending.Path)
				if pending.PermanentFail {
					state.PermanentFailCount++
					state.PermanentFail = append(state.PermanentFail, pending.Path)
				}
			}
			for _, upload := range mountState.ActiveUploads() {
				state.Uploads = append(state.Uploads, FSUpload{
					Path:          upload.Path,
					State:         upload.State,
					BytesTotal:    upload.BytesTotal,
					BytesUploaded: upload.BytesUploaded,
					RetryCount:    upload.RetryCount,
					LastError:     upload.LastError,
					ErrorCategory: upload.ErrorCategory,
				})
			}
			return state
		}
	}
	state.PendingCount = pendingCount(fs)
	for _, pending := range pendingFiles(fs) {
		state.Pending = append(state.Pending, pending.Path)
		if pending.PermanentFail {
			state.PermanentFailCount++
			state.PermanentFail = append(state.PermanentFail, pending.Path)
		}
	}
	return state
}

func debugMountNameMatches(actual, requested string) bool {
	return actual == requested
}

func fsFinalMountState(ctx context.Context, fs any, mount string) *FSMountState {
	state := fsMountState(fs, mount)
	inspector, ok := fs.(diagnostics.DebugStagingInspector)
	if !ok {
		return state
	}
	report, err := inspector.DebugStaging(ctx, "/")
	if err != nil {
		return state
	}
	for _, mountState := range report.Mounts {
		if mountState.Mount != mount {
			continue
		}
		state.StagingOrphans = mountState.OrphanCount
		for _, file := range mountState.Files {
			if file.Pending && file.Exists && !file.SizeMatches {
				state.StagingSizeMismatches++
			}
		}
		for _, file := range mountState.Orphans {
			if file.Pending && file.Exists && !file.SizeMatches {
				state.StagingSizeMismatches++
			}
		}
		return state
	}
	return state
}

func readVFSFile(ctx context.Context, fs vfs.FileSystem, path string) ([]byte, error) {
	rc, err := fs.Read(ctx, path, 0, 0)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func cleanupVFSPath(ctx context.Context, fs vfs.FileSystem, path string, dir bool) error {
	if dir {
		return fs.RemoveDir(ctx, path)
	}
	return fs.Remove(ctx, path)
}

// cleanupVFSProbeFixture removes the probe's temporary file and directory.
// It is best-effort and idempotent: every step tolerates already-removed
// paths so the successful path can leave cleanup to its explicit steps while
// early failures still get a clean remote. Pending generations under dir are
// discarded before the directory removal because a live pending upload makes
// RemoveDir fail with ErrDirNotEmpty.
func cleanupVFSProbeFixture(ctx context.Context, fs vfs.FileSystem, mount, dir, file string) error {
	var errs []error
	if err := fs.Remove(ctx, file); err != nil && !errors.Is(err, vfs.ErrNotFound) {
		errs = append(errs, fmt.Errorf("remove test file: %w", err))
	}
	discardPendingUnder(fs, dir, &errs)
	if err := waitVFSSmokeIdle(ctx, fs, mount, 10*time.Second, nil); err != nil {
		errs = append(errs, fmt.Errorf("wait for test uploads: %w", err))
	}
	if err := fs.RemoveDir(ctx, dir); err != nil && !errors.Is(err, vfs.ErrNotFound) {
		errs = append(errs, fmt.Errorf("remove test directory: %w", err))
	}
	if err := waitVFSSmokeIdle(ctx, fs, mount, 10*time.Second, nil); err != nil {
		errs = append(errs, fmt.Errorf("wait for test directory removal: %w", err))
	}
	return errors.Join(errs...)
}

func discardPendingUnder(fs any, dir string, errs *[]error) {
	inspector, ok := fs.(vfs.UploadInspector)
	if !ok {
		return
	}
	discarder, ok := fs.(vfs.UploadDiscarder)
	if !ok {
		return
	}
	for _, pending := range inspector.PendingUploads() {
		if pending.Path != dir && !strings.HasPrefix(pending.Path, dir+"/") {
			continue
		}
		if err := discarder.DiscardUpload(pending.Path); err != nil {
			*errs = append(*errs, fmt.Errorf("discard upload %s: %w", pending.Path, err))
		}
	}
}

func permanentFailureError(state *FSMountState) error {
	if state == nil || state.PermanentFailCount == 0 {
		return nil
	}
	return fmt.Errorf("VFS has %d permanently failed upload(s): %s; run `qrypt fs journal` to inspect or `qrypt fs journal prune --failed` to discard them",
		state.PermanentFailCount, strings.Join(state.PermanentFail, ", "))
}

func randomSuffix(n int) string {
	if n <= 0 {
		n = 6
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", buf)
}

func fsTestBasePath(fs vfs.FileSystem, mount string) string {
	snapshotter, ok := fs.(vfsDebugSnapshotter)
	if !ok {
		return "/" + mount
	}
	snapshot := snapshotter.DebugSnapshot()
	if snapshot.Kind == "namespace" {
		return "/" + mount
	}
	return ""
}
