package control

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const resumeTestDefaultSize = 64 << 20
const resumeTestWriteChunk = 1 << 20

type ResumeTestResult struct {
	OpID            string            `json:"op_id"`
	Mount           string            `json:"mount"`
	Pass            bool              `json:"pass"`
	Size            int64             `json:"size"`
	Path            string            `json:"path"`
	Fault           *ResumeTestFault  `json:"fault,omitempty"`
	Outcome         string            `json:"outcome,omitempty"`
	Steps           []FSTestStep      `json:"steps"`
	PendingTimeline []FSPendingSample `json:"pending_timeline,omitempty"`
	FinalState      *FSMountState     `json:"final_state,omitempty"`
	Started         time.Time         `json:"started_at"`
	Finished        time.Time         `json:"finished_at"`
	Duration        string            `json:"duration"`
	DurationMS      int64             `json:"duration_ms"`
	RetryCommand    string            `json:"retry_command,omitempty"`
}

type ResumeTestFault struct {
	ID        string `json:"id,omitempty"`
	Triggered bool   `json:"triggered"`
	Error     string `json:"error,omitempty"`
}

func RunVFSResumeTest(ctx context.Context, fs vfs.FileSystem, mount string, size int64) *ResumeTestResult {
	if size <= 0 {
		size = resumeTestDefaultSize
	}
	result := &ResumeTestResult{
		OpID:         newDebugOperationID("resume"),
		Mount:        mount,
		Size:         size,
		Started:      time.Now(),
		Steps:        make([]FSTestStep, 0, 8),
		RetryCommand: fmt.Sprintf("qrypt debug test resume --mount %s --socket PATH --size %d", mount, size),
	}
	defer func() {
		result.Finished = time.Now()
		duration := result.Finished.Sub(result.Started)
		result.Duration = duration.String()
		result.DurationMS = durationMillis(duration)
		result.FinalState = fsFinalMountState(ctx, fs, mount)
		result.Pass = true
		for _, step := range result.Steps {
			if !step.OK {
				result.Pass = false
				break
			}
		}
		if result.Fault == nil || !result.Fault.Triggered {
			result.Pass = false
		}
	}()

	basePath := fsTestBasePath(fs, mount)
	dir := basePath + "/__qrypt_resume_test_" + randomSuffix(6)
	file := dir + "/resume.bin"
	result.Path = file

	step := fsStep("mkdir")
	step.Input = map[string]any{"path": dir}
	start := time.Now()
	_, err := fs.Mkdir(ctx, dir)
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		return result
	}
	defer cleanupVFSPath(context.WithoutCancel(ctx), fs, dir, true)

	step = fsStep("write")
	step.Input = map[string]any{"path": file, "bytes": size}
	start = time.Now()
	err = writeRandomVFSFile(ctx, fs, file, size)
	step.Actual = map[string]any{"pending_count": len(fs.Pending())}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		return result
	}
	defer cleanupVFSPath(context.WithoutCancel(ctx), fs, file, false)

	step = fsStep("inject_cancel")
	start = time.Now()
	injector, ok := fs.(vfs.DebugUploadCancelInjector)
	if !ok {
		err = fmt.Errorf("VFS resume test not available: filesystem does not support debug upload cancel injection")
	} else {
		var injected vfs.DebugUploadCancelResult
		afterBytes := minInt64(size/4, 16<<20)
		if afterBytes < 1 {
			afterBytes = 1
		}
		injected, err = injector.DebugInjectUploadCancel(ctx, vfs.DebugUploadCancelRequest{
			Path:       file,
			Phase:      drive.UploadPhaseUploading,
			AfterBytes: afterBytes,
			Once:       true,
			Reason:     "debug_resume_test",
		})
		if err == nil {
			result.Fault = &ResumeTestFault{ID: injected.ID}
			step.Actual = map[string]any{"fault_id": injected.ID}
		}
	}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
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
		return result
	}

	step = fsStep("wait_cancel")
	step.Expected = map[string]any{"context_canceled": true}
	start = time.Now()
	err = waitVFSUploadCanceled(ctx, fs, mount, file, 2*time.Minute, &result.PendingTimeline)
	triggered := err == nil
	if result.Fault != nil {
		result.Fault.Triggered = triggered
		if err != nil {
			result.Fault.Error = err.Error()
		}
	}
	step.Actual = map[string]any{"triggered": triggered, "state": fsMountState(fs, mount)}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		return result
	}

	step = fsStep("wait_resume")
	step.Expected = map[string]any{"pending_count": 0, "upload_count": 0}
	start = time.Now()
	err = waitVFSSmokeIdle(ctx, fs, mount, 10*time.Minute, &result.PendingTimeline)
	step.Actual = map[string]any{"state": fsMountState(fs, mount)}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		result.Outcome = "failed_after_cancel"
		return result
	}

	step = fsStep("stat")
	step.Input = map[string]any{"path": file}
	step.Expected = map[string]any{"size": size}
	start = time.Now()
	entry, err := fs.Stat(ctx, file)
	if err == nil && entry.Size != size {
		err = fmt.Errorf("uploaded size = %d, want %d", entry.Size, size)
	}
	step.Actual = map[string]any{"size": entry.Size}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		result.Outcome = "uploaded_size_mismatch"
		return result
	}
	result.Outcome = resumeOutcome(fs, mount, file)
	return result
}

func writeRandomVFSFile(ctx context.Context, fs vfs.FileSystem, path string, size int64) error {
	if err := fs.Create(ctx, path); err != nil {
		return err
	}
	buf := make([]byte, resumeTestWriteChunk)
	var off int64
	for off < size {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n := int64(len(buf))
		if remaining := size - off; remaining < n {
			n = remaining
		}
		chunk := buf[:n]
		if _, err := rand.Read(chunk); err != nil {
			return err
		}
		written, err := fs.WriteAt(ctx, path, chunk, off)
		if err != nil {
			return err
		}
		if written <= 0 {
			return fmt.Errorf("VFS write made no progress at offset %d", off)
		}
		off += int64(written)
	}
	return nil
}

func waitVFSUploadCanceled(ctx context.Context, fs vfs.FileSystem, mount, path string, timeout time.Duration, timeline *[]FSPendingSample) error {
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
		if uploadCanceled(fs, mount, path) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("timeout waiting for debug upload cancel after %s", timeout)
		case <-ticker.C:
		}
	}
}

func uploadCanceled(fs vfs.FileSystem, mount, path string) bool {
	snapshotter, ok := fs.(vfsDebugSnapshotter)
	if !ok {
		for _, pending := range fs.Pending() {
			if sameDebugPath(pending.Path, path) && strings.Contains(pending.LastError, "context canceled") {
				return true
			}
		}
		return false
	}
	snapshot := snapshotter.DebugSnapshot()
	for _, mountState := range snapshot.Mounts {
		if !debugMountNameMatches(mountState.Identity.Name, mount) {
			continue
		}
		for _, upload := range mountState.UploadState.History {
			if sameDebugPath(upload.Path, path) && strings.Contains(upload.LastError, "context canceled") {
				return true
			}
		}
		for _, pending := range mountState.PendingFiles() {
			if sameDebugPath(pending.Path, path) && strings.Contains(pending.LastError, "context canceled") {
				return true
			}
		}
	}
	return false
}

func resumeOutcome(fs vfs.FileSystem, mount, path string) string {
	snapshotter, ok := fs.(vfsDebugSnapshotter)
	if !ok {
		return "completed"
	}
	var sawInvalidSession bool
	snapshot := snapshotter.DebugSnapshot()
	for _, mountState := range snapshot.Mounts {
		if !debugMountNameMatches(mountState.Identity.Name, mount) {
			continue
		}
		for _, upload := range mountState.UploadState.History {
			if !sameDebugPath(upload.Path, path) {
				continue
			}
			if strings.Contains(upload.LastError, "resumed upload session invalid") {
				sawInvalidSession = true
			}
		}
	}
	if sawInvalidSession {
		return "resume_session_invalid_fallback"
	}
	return "resumed_or_completed"
}

func sameDebugPath(got, want string) bool {
	if got == want {
		return true
	}
	got = strings.TrimSuffix(got, "/")
	want = strings.TrimSuffix(want, "/")
	return got == want || strings.HasSuffix(got, want) || strings.HasSuffix(want, got)
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
