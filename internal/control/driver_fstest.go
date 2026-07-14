package control

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
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
	Attempt        int        `json:"attempt"`
	Elapsed        string     `json:"elapsed"`
	ElapsedMS      int64      `json:"elapsed_ms"`
	PendingCount   int        `json:"pending_count"`
	UploadCount    int        `json:"upload_count"`
	DeleteTimers   int        `json:"delete_timer_count"`
	Pending        []string   `json:"pending,omitempty"`
	Uploads        []FSUpload `json:"uploads,omitempty"`
	DeleteTimerFor []string   `json:"delete_timers,omitempty"`
	Error          string     `json:"error,omitempty"`
}

type FSMountState struct {
	Mount        string     `json:"mount"`
	PendingCount int        `json:"pending_count"`
	UploadCount  int        `json:"upload_count"`
	DeleteTimers int        `json:"delete_timer_count"`
	Pending      []string   `json:"pending,omitempty"`
	Uploads      []FSUpload `json:"uploads,omitempty"`
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
		RetryCommand: fmt.Sprintf("qrypt debug test fs --mount %s --socket PATH", mount),
	}
	defer func() {
		result.Finished = time.Now()
		duration := result.Finished.Sub(result.Started)
		result.Duration = duration.String()
		result.DurationMS = durationMillis(duration)
		result.FinalState = fsMountState(fs, mount)
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

	step = fsStep("write")
	step.Input = map[string]any{"path": file, "bytes": len(data)}
	step.Expected = map[string]any{"pending_created": true}
	start = time.Now()
	err = fs.Create(ctx, file)
	if err == nil {
		_, err = fs.WriteAt(ctx, file, data, 0)
	}
	step.Actual = map[string]any{"pending_count": len(fs.Pending())}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		cleanupVFSPath(ctx, fs, dir, true)
		return result
	}

	step = fsStep("flush")
	step.Input = map[string]any{"path": file}
	start = time.Now()
	err = fs.Flush(ctx, file)
	step.Actual = map[string]any{"pending_count": len(fs.Pending())}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		cleanupVFSPath(ctx, fs, dir, true)
		return result
	}

	step = fsStep("wait_upload")
	step.Expected = map[string]any{"pending_count": 0, "upload_count": 0}
	start = time.Now()
	err = waitVFSSmokeIdle(ctx, fs, mount, 2*time.Minute, &result.PendingTimeline)
	step.Actual = map[string]any{"state": fsMountState(fs, mount)}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		cleanupVFSPath(ctx, fs, dir, true)
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
	s.DurationMS = durationMillis(duration)
	if err != nil {
		s.OK = false
		s.Error = err.Error()
		s.ErrorCategory = drive.ErrorCategory(err)
		return
	}
	s.OK = true
}

func waitVFSSmokeIdle(ctx context.Context, fs vfs.FileSystem, mount string, timeout time.Duration, timeline *[]FSPendingSample) error {
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
		if sample.PendingCount == 0 && sample.UploadCount == 0 && sample.DeleteTimers == 0 {
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

func fsPendingSample(fs vfs.FileSystem, mount string, attempt int, elapsed time.Duration) FSPendingSample {
	state := fsMountState(fs, mount)
	sample := FSPendingSample{
		Attempt:      attempt,
		Elapsed:      elapsed.String(),
		ElapsedMS:    durationMillis(elapsed),
		PendingCount: state.PendingCount,
		UploadCount:  state.UploadCount,
		DeleteTimers: state.DeleteTimers,
		Pending:      state.Pending,
		Uploads:      state.Uploads,
	}
	if snapshotter, ok := fs.(vfsDebugSnapshotter); ok {
		for _, mountState := range snapshotter.DebugSnapshot().Mounts {
			if mountState.Name != mount {
				continue
			}
			for _, timer := range mountState.DeleteTimers {
				sample.DeleteTimerFor = append(sample.DeleteTimerFor, timer.Path)
			}
		}
	}
	return sample
}

func fsMountState(fs vfs.FileSystem, mount string) *FSMountState {
	state := &FSMountState{Mount: mount}
	if snapshotter, ok := fs.(vfsDebugSnapshotter); ok {
		for _, mountState := range snapshotter.DebugSnapshot().Mounts {
			if mountState.Name != mount {
				continue
			}
			state.PendingCount = len(mountState.Pending)
			state.UploadCount = len(mountState.Uploads)
			state.DeleteTimers = len(mountState.DeleteTimers)
			for _, pending := range mountState.Pending {
				state.Pending = append(state.Pending, pending.Path)
			}
			for _, upload := range mountState.Uploads {
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
	state.PendingCount = len(fs.Pending())
	for _, pending := range fs.Pending() {
		state.Pending = append(state.Pending, pending.Path)
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
