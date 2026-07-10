package control

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// RunVFSXferTest executes a VFS-level transfer test between two mount points.
// It generates random data, writes to the source mount through VFS staging,
// waits for the source upload, reads back, writes to the dest mount through
// VFS staging, waits for the dest upload, and returns quantified metrics.
func RunVFSXferTest(ctx context.Context, fs vfs.FileSystem, srcMount, dstMount string, size int64) *XferTestResult {
	result := &XferTestResult{
		SourceMount: srcMount,
		DestMount:   dstMount,
		VFS:         true,
		Started:     time.Now(),
		Steps:       make([]XferTestStep, 0, 10),
	}
	if size <= 0 {
		size = 1 << 20
	}

	testSuffix := make([]byte, 6)
	if _, err := rand.Read(testSuffix); err != nil {
		result.Steps = append(result.Steps, XferTestStep{
			Phase: "generate_name", OK: false, Error: err.Error(), Duration: "0s",
		})
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	testName := fmt.Sprintf("__qrypt_xfer_test_%x", testSuffix)
	_ = testName

	// Build VFS paths.
	srcDir := "/" + srcMount + "/" + testName
	srcPath := srcDir + "/data.bin"
	dstDir := "/" + dstMount + "/" + testName
	dstPath := dstDir + "/data.bin"

	// generate test data
	data := make([]byte, size)
	s := XferTestStep{Phase: "generate_data"}
	t0 := time.Now()
	if _, err := rand.Read(data); err != nil {
		finishXferStep(&s, t0, err)
		result.Steps = append(result.Steps, s)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	finishXferStep(&s, t0, nil)
	s.Bytes = size
	result.Steps = append(result.Steps, s)

	// mkdir on source
	s = XferTestStep{Phase: "mkdir_source"}
	t0 = time.Now()
	_, err := fs.Mkdir(ctx, srcDir)
	finishXferStep(&s, t0, err)
	result.Steps = append(result.Steps, s)
	if err != nil {
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// staging write to source
	s = XferTestStep{Phase: "staging_write_source"}
	t0 = time.Now()
	if err := fs.Create(ctx, srcPath); err != nil {
		finishXferStep(&s, t0, err)
		result.Steps = append(result.Steps, s)
		cleanupPaths(ctx, fs, srcDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	writeChunks := 0
	chunkSize := int64(256 * 1024)
	for off := int64(0); off < size; off += chunkSize {
		end := off + chunkSize
		if end > size {
			end = size
		}
		if _, err := fs.WriteAt(ctx, srcPath, data[off:end], off); err != nil {
			finishXferStep(&s, t0, err)
			result.Steps = append(result.Steps, s)
			cleanupPaths(ctx, fs, srcDir)
			result.Pass = false
			result.Finished = time.Now()
			return result
		}
		writeChunks++
	}
	finishXferStep(&s, t0, nil)
	s.Bytes = size
	result.Steps = append(result.Steps, s)
	result.Metrics.StagingWriteTime = s.Duration

	// flush source (enqueue upload)
	s = XferTestStep{Phase: "flush_source"}
	t0 = time.Now()
	if err := fs.Flush(ctx, srcPath); err != nil {
		finishXferStep(&s, t0, err)
		result.Steps = append(result.Steps, s)
		cleanupPaths(ctx, fs, srcDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	finishXferStep(&s, t0, nil)
	result.Steps = append(result.Steps, s)
	result.Metrics.FlushTime = s.Duration

	// wait for source upload
	s = XferTestStep{Phase: "wait_upload_source"}
	t0 = time.Now()
	if err := waitVFSIdle(ctx, fs, 5*time.Minute); err != nil {
		finishXferStep(&s, t0, err)
		result.Steps = append(result.Steps, s)
		cleanupPaths(ctx, fs, srcDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	finishXferStep(&s, t0, nil)
	result.Steps = append(result.Steps, s)
	result.Metrics.UploadSourceTime = s.Duration
	appendVFSUploadTimeline(result, fs, srcPath, "source")

	// read from source
	s = XferTestStep{Phase: "read_source"}
	t0 = time.Now()
	rc, err := fs.Read(ctx, srcPath, 0, 0)
	if err != nil {
		finishXferStep(&s, t0, err)
		result.Steps = append(result.Steps, s)
		cleanupPaths(ctx, fs, srcDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	readBuf := bytes.NewBuffer(make([]byte, 0, size))
	readBytes, readErr := io.Copy(readBuf, rc)
	if closeErr := rc.Close(); readErr == nil {
		readErr = closeErr
	}
	sourceData := readBuf.Bytes()
	if readErr == nil && readBytes != size {
		readErr = fmt.Errorf("source read size mismatch: got %d bytes, want %d", readBytes, size)
	}
	if readErr == nil && !bytes.Equal(data, sourceData) {
		readErr = fmt.Errorf("source content mismatch: got %d bytes, want %d", len(sourceData), len(data))
	}
	finishXferStep(&s, t0, readErr)
	s.Bytes = readBytes
	result.Steps = append(result.Steps, s)
	result.Metrics.ReadTime = s.Duration
	if d, _ := time.ParseDuration(s.Duration); d > 0 {
		result.Metrics.ReadThroughput = int64(float64(readBytes) / d.Seconds())
	}
	result.Metrics.ReadChunks = 1

	if readErr != nil {
		cleanupPaths(ctx, fs, srcDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// mkdir on dest
	s = XferTestStep{Phase: "mkdir_dest"}
	t0 = time.Now()
	_, err = fs.Mkdir(ctx, dstDir)
	finishXferStep(&s, t0, err)
	result.Steps = append(result.Steps, s)
	if err != nil {
		cleanupPaths(ctx, fs, srcDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// staging write to dest
	s = XferTestStep{Phase: "staging_write_dest"}
	t0 = time.Now()
	if err := fs.Create(ctx, dstPath); err != nil {
		finishXferStep(&s, t0, err)
		result.Steps = append(result.Steps, s)
		cleanupPaths(ctx, fs, srcDir)
		cleanupPaths(ctx, fs, dstDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	for off := int64(0); off < size; off += chunkSize {
		end := off + chunkSize
		if end > size {
			end = size
		}
		if _, err := fs.WriteAt(ctx, dstPath, sourceData[off:end], off); err != nil {
			finishXferStep(&s, t0, err)
			result.Steps = append(result.Steps, s)
			cleanupPaths(ctx, fs, srcDir)
			cleanupPaths(ctx, fs, dstDir)
			result.Pass = false
			result.Finished = time.Now()
			return result
		}
	}
	finishXferStep(&s, t0, nil)
	s.Bytes = size
	result.Steps = append(result.Steps, s)

	// flush dest (enqueue upload)
	s = XferTestStep{Phase: "flush_dest"}
	t0 = time.Now()
	if err := fs.Flush(ctx, dstPath); err != nil {
		finishXferStep(&s, t0, err)
		result.Steps = append(result.Steps, s)
		cleanupPaths(ctx, fs, srcDir)
		cleanupPaths(ctx, fs, dstDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	finishXferStep(&s, t0, nil)
	result.Steps = append(result.Steps, s)

	// wait for dest upload
	s = XferTestStep{Phase: "wait_upload_dest"}
	t0 = time.Now()
	if err := waitVFSIdle(ctx, fs, 5*time.Minute); err != nil {
		finishXferStep(&s, t0, err)
		result.Steps = append(result.Steps, s)
		cleanupPaths(ctx, fs, srcDir)
		cleanupPaths(ctx, fs, dstDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	finishXferStep(&s, t0, nil)
	result.Steps = append(result.Steps, s)
	result.Metrics.UploadDestTime = s.Duration
	appendVFSUploadTimeline(result, fs, dstPath, "dest")

	// verify content
	s = XferTestStep{Phase: "verify_data"}
	t0 = time.Now()
	verifyErr := error(nil)
	rc, err = fs.Read(ctx, dstPath, 0, 0)
	if err != nil {
		verifyErr = fmt.Errorf("dest read for verify: %w", err)
	} else {
		dstData, rErr := io.ReadAll(rc)
		rc.Close()
		if rErr != nil {
			verifyErr = fmt.Errorf("dest read body: %w", rErr)
		} else if !bytes.Equal(data, dstData) {
			verifyErr = fmt.Errorf("content mismatch: got %d bytes, want %d", len(dstData), len(data))
		}
	}
	finishXferStep(&s, t0, verifyErr)
	s.Bytes = size
	result.Steps = append(result.Steps, s)

	// cleanup
	cleanupPaths(ctx, fs, srcDir)
	cleanupPaths(ctx, fs, dstDir)

	// compute metrics
	result.Metrics.TotalBytes = size
	result.Metrics.WriteChunks = writeChunks
	result.Metrics.WriteTime = result.Metrics.StagingWriteTime
	result.Metrics.CreateTime = result.Metrics.StagingWriteTime
	result.Metrics.WallTime = time.Since(result.Started).String()
	if d, _ := time.ParseDuration(result.Metrics.StagingWriteTime); d > 0 {
		result.Metrics.WriteThroughput = int64(float64(size) / d.Seconds())
	}

	result.Pass = true
	for _, step := range result.Steps {
		if !step.OK {
			result.Pass = false
			break
		}
	}
	result.Finished = time.Now()
	return result
}

func waitVFSIdle(ctx context.Context, fs vfs.FileSystem, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if len(fs.Pending()) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("timeout waiting for pending operations after %s", timeout)
		case <-ticker.C:
		}
	}
}

func cleanupPaths(ctx context.Context, fs vfs.FileSystem, path string) {
	_ = fs.RemoveDir(ctx, path)
}

type vfsDebugSnapshotter interface {
	DebugSnapshot() vfs.DebugSnapshot
}

func appendVFSUploadTimeline(result *XferTestResult, fs vfs.FileSystem, path, role string) {
	snapshotter, ok := fs.(vfsDebugSnapshotter)
	if !ok {
		return
	}
	snapshot := snapshotter.DebugSnapshot()
	upload, mountName, driverName, ok := findVFSUpload(snapshot, path)
	if !ok {
		return
	}
	if !upload.StartedAt.IsZero() && !upload.CompletedAt.IsZero() {
		duration := upload.CompletedAt.Sub(upload.StartedAt)
		event := XferTraceEvent{
			Phase:      "upload_total",
			Mount:      mountName,
			Driver:     driverName,
			Path:       path,
			Bytes:      upload.BytesTotal,
			Duration:   duration.String(),
			StartedAt:  upload.StartedAt,
			FinishedAt: upload.CompletedAt,
			Extra: map[string]any{
				"role":            role,
				"state":           upload.State,
				"bytes_uploaded":  upload.BytesUploaded,
				"stage_durations": upload.StageDurations,
			},
		}
		if upload.BytesTotal > 0 && duration > 0 {
			event.Throughput = int64(float64(upload.BytesTotal) / duration.Seconds())
		}
		result.Timeline = append(result.Timeline, event)
	}
	for _, trace := range upload.Trace {
		event := XferTraceEvent{
			Phase:      trace.Phase,
			Mount:      mountName,
			Driver:     driverName,
			Path:       path,
			Bytes:      trace.Bytes,
			Duration:   trace.Duration,
			Throughput: trace.Throughput,
			StartedAt:  trace.StartedAt,
			FinishedAt: trace.FinishedAt,
			Extra:      cloneTraceExtra(trace.Extra),
		}
		if event.Extra == nil {
			event.Extra = map[string]any{}
		}
		event.Extra["role"] = role
		result.Timeline = append(result.Timeline, event)
	}
}

func findVFSUpload(snapshot vfs.DebugSnapshot, path string) (vfs.DebugUpload, string, string, bool) {
	for _, mount := range snapshot.Mounts {
		localPath := path
		if snapshot.Kind == "namespace" {
			prefix := "/" + mount.Name
			if path != prefix && !strings.HasPrefix(path, prefix+"/") {
				continue
			}
			localPath = strings.TrimPrefix(path, prefix)
			if localPath == "" {
				localPath = "/"
			}
		}
		if upload, ok := findDebugUpload(mount.UploadHistory, localPath); ok {
			return upload, mount.Name, mount.DriverName, true
		}
		if upload, ok := findDebugUpload(mount.Uploads, localPath); ok {
			return upload, mount.Name, mount.DriverName, true
		}
	}
	return vfs.DebugUpload{}, "", "", false
}

func findDebugUpload(uploads []vfs.DebugUpload, path string) (vfs.DebugUpload, bool) {
	for i := len(uploads) - 1; i >= 0; i-- {
		if cleanVirtual(uploads[i].Path) == cleanVirtual(path) {
			return uploads[i], true
		}
	}
	return vfs.DebugUpload{}, false
}

func cloneTraceExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string]any, len(extra))
	for k, v := range extra {
		out[k] = v
	}
	return out
}
