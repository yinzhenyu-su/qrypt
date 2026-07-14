package control

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// XferTestMetrics contains the quantitative metrics for a transfer test.
type XferTestMetrics struct {
	TotalBytes      int64  `json:"total_bytes"`
	WallTime        string `json:"wall_time"`
	ReadTime        string `json:"read_time"`
	WriteTime       string `json:"write_time"`
	ReadChunks      int    `json:"read_chunks"`
	WriteChunks     int    `json:"write_chunks"`
	ReadThroughput  int64  `json:"read_throughput"`  // bytes/sec
	WriteThroughput int64  `json:"write_throughput"` // bytes/sec

	// VFS-layer metrics (driver-layer omits these)
	CreateTime       string `json:"create_time,omitempty"`
	FlushTime        string `json:"flush_time,omitempty"`
	StagingWriteTime string `json:"staging_write_time,omitempty"`
	UploadSourceTime string `json:"upload_source_time,omitempty"`
	UploadDestTime   string `json:"upload_dest_time,omitempty"`
}

// TransferTraceEvent uses the same schema as runtime VFS operation events.
type TransferTraceEvent = vfs.DebugOperationEvent

// TransferStep records one phase of a transfer test.
type TransferStep struct {
	Phase         string `json:"phase"`
	OK            bool   `json:"ok"`
	Error         string `json:"error,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
	Duration      string `json:"duration"`
	Bytes         int64  `json:"bytes,omitempty"`
}

// XferTestResult aggregates the full transfer test outcome.
type XferTestResult struct {
	OpID        string               `json:"op_id"`
	SourceMount string               `json:"source_mount"`
	DestMount   string               `json:"dest_mount"`
	SourceType  string               `json:"source_type,omitempty"`
	DestType    string               `json:"dest_type,omitempty"`
	VFS         bool                 `json:"vfs"`
	Pass        bool                 `json:"pass"`
	Steps       []TransferStep       `json:"steps"`
	Started     time.Time            `json:"started_at"`
	Finished    time.Time            `json:"finished_at"`
	Metrics     XferTestMetrics      `json:"metrics"`
	Timeline    []TransferTraceEvent `json:"timeline,omitempty"`
}

var debugOperationSequence atomic.Uint64

func newDebugOperationID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), debugOperationSequence.Add(1))
}

func xferStepOp(phase string) TransferStep {
	return TransferStep{Phase: phase, Duration: "0s"}
}

func (s *TransferStep) done(err error) {
	if err != nil {
		s.OK = false
		s.Error = err.Error()
		s.ErrorCategory = drive.ErrorCategory(err)
	} else {
		s.OK = true
	}
}

func (s *TransferStep) finish(start time.Time, err error) {
	s.Duration = time.Since(start).String()
	s.done(err)
}

// finishTransferStep records the duration and error on a step.
func finishTransferStep(s *TransferStep, start time.Time, err error) {
	s.Duration = time.Since(start).String()
	if err != nil {
		s.OK = false
		s.Error = err.Error()
		s.ErrorCategory = drive.ErrorCategory(err)
	} else {
		s.OK = true
	}
}

// xferTestSize returns the test data size in bytes.
func xferTestSize(size int64) int64 {
	if size <= 0 {
		return 1 << 20 // default 1 MiB
	}
	return size
}

// RunDriverXferTest executes a driver-level transfer test between two drivers.
// It generates random data, writes it to the source driver, reads it back
// with chunked reads, and writes to the destination driver.
func RunDriverXferTest(ctx context.Context, srcMount string, srcDriver drive.Driver, dstMount string, dstDriver drive.Driver, size int64) *XferTestResult {
	result := &XferTestResult{
		OpID:        newDebugOperationID("xfer"),
		SourceMount: srcMount,
		DestMount:   dstMount,
		VFS:         false,
		Started:     time.Now(),
		Steps:       make([]TransferStep, 0, 8),
	}
	size = xferTestSize(size)

	// Collect driver type info.
	if debugger, ok := srcDriver.(drive.Debugger); ok {
		if snap, err := debugger.DebugSnapshot(ctx); err == nil {
			result.SourceType = snap.Driver
		}
	}
	if debugger, ok := dstDriver.(drive.Debugger); ok {
		if snap, err := debugger.DebugSnapshot(ctx); err == nil {
			result.DestType = snap.Driver
		}
	}

	// Check capabilities.
	srcUploader, srcHasUploader := srcDriver.(drive.SourceUploader)
	srcWriter, srcIsWriter := srcDriver.(drive.Writer)
	dstUploader, dstHasUploader := dstDriver.(drive.SourceUploader)
	dstWriter, dstIsWriter := dstDriver.(drive.Writer)

	if !srcIsWriter || !srcHasUploader {
		result.Steps = append(result.Steps, TransferStep{
			Phase: "capability_check", OK: false,
			Error: "source driver does not implement Writer and SourceUploader", ErrorCategory: drive.ErrorCategoryUnsupported, Duration: "0s",
		})
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	if !dstIsWriter || !dstHasUploader {
		result.Steps = append(result.Steps, TransferStep{
			Phase: "capability_check", OK: false,
			Error: "dest driver does not implement Writer and SourceUploader", ErrorCategory: drive.ErrorCategoryUnsupported, Duration: "0s",
		})
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// Unique test directory name.
	testSuffix := make([]byte, 6)
	if _, err := rand.Read(testSuffix); err != nil {
		result.Steps = append(result.Steps, TransferStep{
			Phase: "generate_name", OK: false, Error: err.Error(), ErrorCategory: drive.ErrorCategory(err), Duration: "0s",
		})
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	testName := fmt.Sprintf("__qrypt_xfer_test_%x", testSuffix)
	fileName := "data.bin"

	// ---------- generate test data ----------
	data := make([]byte, size)
	step := xferStepOp("generate_data")
	t0 := time.Now()
	if _, err := rand.Read(data); err != nil {
		step.finish(t0, err)
		result.Steps = append(result.Steps, step)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	step.finish(t0, nil)
	step.Bytes = size
	result.Steps = append(result.Steps, step)

	// ---------- mkdir on source ----------
	step = xferStepOp("mkdir_source")
	t0 = time.Now()
	srcRootID := driverProbeRootID(ctx, srcDriver)
	srcDir, err := srcWriter.Mkdir(ctx, srcRootID, testName)
	step.finish(t0, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// ---------- write to source (PutSource) ----------
	step = xferStepOp("write_source")
	t0 = time.Now()
	srcEntry, err := srcUploader.PutSource(ctx, drive.UploadRequest{
		ParentID: srcDir.ID,
		Name:     fileName,
		Source:   drive.NewBytesReadOnlyFileSource(data),
	})
	step.finish(t0, err)
	step.Bytes = size
	result.Steps = append(result.Steps, step)
	if err != nil {
		cleanupProbeDir(ctx, srcWriter, srcDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// ---------- read from source (chunked) ----------
	step = xferStepOp("read_source")
	t0 = time.Now()
	readBytes := int64(0)
	chunkCount := 0
	const readBufSize int64 = 256 * 1024
	readBuf := make([]byte, readBufSize)
	verifyBuf := make([]byte, size) // for verification
	readErr := error(nil)

	for offset := int64(0); offset < size; offset += readBufSize {
		chunkSize := readBufSize
		if offset+chunkSize > size {
			chunkSize = size - offset
		}
		rc, err := srcDriver.Read(ctx, srcEntry, offset, chunkSize)
		if err != nil {
			readErr = fmt.Errorf("read at offset %d: %w", offset, err)
			break
		}
		n, err := io.ReadFull(rc, readBuf[:chunkSize])
		rc.Close()
		if err != nil && err != io.ErrUnexpectedEOF {
			readErr = fmt.Errorf("read body at offset %d: %w", offset, err)
			break
		}
		copy(verifyBuf[offset:], readBuf[:n])
		readBytes += int64(n)
		chunkCount++
	}
	step.finish(t0, readErr)
	step.Bytes = readBytes
	result.Steps = append(result.Steps, step)
	if readErr != nil {
		cleanupProbeDir(ctx, srcWriter, srcDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// ---------- mkdir on dest ----------
	step = xferStepOp("mkdir_dest")
	t0 = time.Now()
	dstRootID := driverProbeRootID(ctx, dstDriver)
	dstDir, err := dstWriter.Mkdir(ctx, dstRootID, testName)
	step.finish(t0, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		cleanupProbeDir(ctx, srcWriter, srcDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// ---------- write to dest (PutSource) ----------
	step = xferStepOp("write_dest")
	t0 = time.Now()
	dstEntry, err := dstUploader.PutSource(ctx, drive.UploadRequest{
		ParentID: dstDir.ID,
		Name:     fileName,
		Source:   drive.NewBytesReadOnlyFileSource(data),
	})
	step.finish(t0, err)
	step.Bytes = size
	result.Steps = append(result.Steps, step)
	if err != nil {
		cleanupProbeDir(ctx, srcWriter, srcDir)
		cleanupProbeDir(ctx, dstWriter, dstDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// ---------- verify content ----------
	step = xferStepOp("verify_data")
	t0 = time.Now()
	verifyErr := error(nil)
	if readBytes != size {
		verifyErr = fmt.Errorf("size mismatch: read %d, expected %d", readBytes, size)
	} else {
		rc, err := dstDriver.Read(ctx, dstEntry, 0, size)
		if err != nil {
			verifyErr = fmt.Errorf("dest read for verify: %w", err)
		} else {
			dstData := make([]byte, size)
			n, rErr := io.ReadFull(rc, dstData)
			rc.Close()
			if rErr != nil && rErr != io.ErrUnexpectedEOF {
				verifyErr = fmt.Errorf("dest read body for verify: %w", rErr)
			} else if int64(n) != size {
				verifyErr = fmt.Errorf("dest read size mismatch: got %d, want %d", n, size)
			} else {
				for i := int64(0); i < size; i++ {
					if verifyBuf[i] != dstData[i] {
						verifyErr = fmt.Errorf("content mismatch at byte %d: got %x, want %x", i, dstData[i], verifyBuf[i])
						break
					}
				}
			}
		}
	}
	step.finish(t0, verifyErr)
	step.Bytes = size
	result.Steps = append(result.Steps, step)

	// ---------- cleanup ----------
	cleanupProbeDir(ctx, srcWriter, srcDir)
	cleanupProbeDir(ctx, dstWriter, dstDir)

	// Compute metrics from step durations.
	for _, s := range result.Steps {
		switch s.Phase {
		case "read_source":
			result.Metrics.ReadTime = s.Duration
			d, _ := time.ParseDuration(s.Duration)
			if d > 0 {
				result.Metrics.ReadThroughput = int64(float64(s.Bytes) / d.Seconds())
			}
		case "write_source":
			result.Metrics.WriteTime = s.Duration
			d, _ := time.ParseDuration(s.Duration)
			if d > 0 {
				result.Metrics.WriteThroughput = int64(float64(s.Bytes) / d.Seconds())
			}
		}
	}
	result.Metrics.TotalBytes = size
	result.Metrics.ReadChunks = chunkCount
	result.Metrics.WriteChunks = 1 // PutSource is single upload
	result.Metrics.WallTime = time.Since(result.Started).String()

	// Determine pass/fail.
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
