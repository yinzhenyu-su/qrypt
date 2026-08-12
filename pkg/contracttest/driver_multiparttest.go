package contracttest

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// defaultMultipartSize is large enough to span multiple chunks for every
// supported backend (115 uses 16 MiB parts, others are at or below that).
const defaultMultipartSize = 34 * 1024 * 1024

// RunDriverMultipartTest uploads a single file larger than one backend
// chunk, then verifies list visibility, byte-exact readback, and clean
// removal. This exercises the multipart/chunked upload state machine
// (init → parts → complete, or OSS-style part upload) that small-file CRUD
// cases never reach.
func RunDriverMultipartTest(ctx context.Context, mount string, d drive.Driver, size int64) *CRUDTestResult {
	result := &CRUDTestResult{
		OpID:         newDebugOperationID("multipart"),
		Mount:        mount,
		Started:      time.Now(),
		Steps:        make([]CRUDTestStep, 0, 7),
		RetryCommand: fmt.Sprintf("qrypt debug test multipart --mount %s --socket PATH", mount),
	}
	defer func() {
		result.Finished = time.Now()
		duration := result.Finished.Sub(result.Started)
		result.Duration = duration.String()
		result.DurationMS = DurationMillis(duration)
		if metrics, err := d.Metrics(ctx, result.Started); err == nil {
			result.Metrics = metrics
		}
		result.Pass = true
		for _, step := range result.Steps {
			if !step.OK {
				result.Pass = false
				break
			}
		}
		if len(result.Residual) > 0 {
			result.Pass = false
			result.CleanupGuidance = "manual cleanup may be required for residual remote objects listed in residual[]"
		}
	}()

	if snap, err := d.DebugSnapshot(ctx); err == nil {
		result.Driver = snap.Driver
	}
	if !drive.HasCapability(d, drive.CapabilityWriter) {
		result.addStep(CRUDTestStep{
			Operation:     "multipart",
			OpID:          result.OpID,
			Mount:         result.Mount,
			Driver:        result.Driver,
			OK:            false,
			Error:         "driver does not implement Writer (read-only)",
			ErrorCategory: drive.ErrorCategoryUnsupported,
			Duration:      "0s",
		})
		return result
	}
	if !drive.HasCapability(d, drive.CapabilitySourceUploader) {
		result.addStep(CRUDTestStep{
			Operation:     "multipart",
			OpID:          result.OpID,
			Mount:         result.Mount,
			Driver:        result.Driver,
			OK:            false,
			Error:         "driver does not implement SourceUploader",
			ErrorCategory: drive.ErrorCategoryUnsupported,
			Duration:      "0s",
		})
		return result
	}
	if size <= 0 {
		size = defaultMultipartSize
	}

	// Deterministic pseudo-random payload: large enough to force chunked
	// upload, cheap to generate, and (with overwhelming probability) not
	// already present on the backend, so it cannot short-circuit via the
	// service-side dedup path.
	content := pseudoRandomBytes(0x5eed, int(size))

	s := result.newStep("mkdir", "test-dir")
	start := time.Now()
	fx, err := NewFixture(ctx, d, "multipart")
	name := ""
	if fx != nil {
		name = fx.Name()
	}
	s.Input = map[string]any{"parent_id": driverProbeRootID(ctx, d), "name": name}
	s.Expected = map[string]any{"is_dir": true, "name": name}
	s.Actual = map[string]any{"is_dir": err == nil, "name": name}
	s.finish(start, err)
	result.addStep(s)
	if err != nil {
		return result
	}
	result.addCreated("test_dir", fx.TestDir)

	const fileName = "large.bin"
	s = result.newStep("put", fileName)
	s.Input = map[string]any{"parent_id": fx.TestDir.ID, "name": fileName, "bytes": size}
	s.Expected = map[string]any{"name": fileName, "size": size, "is_dir": false}
	start = time.Now()
	entry, err := d.PutSource(stepContext(ctx, s), drive.UploadRequest{
		ParentID: fx.TestDir.ID,
		Name:     fileName,
		Source:   drive.NewBytesReadOnlyFileSource(content),
	})
	s.Actual = entryActual(entry)
	s.finish(start, err)
	result.addStep(s)
	if err != nil {
		return result
	}
	result.addCreated("file", entry)

	s = result.newStep("verify_put_list", fileName)
	s.Input = map[string]any{"parent_id": fx.TestDir.ID, "name": fileName}
	s.Expected = map[string]any{"listed": true}
	start = time.Now()
	listed, err := fx.VerifyList(stepContext(ctx, s), fx.TestDir.ID, fileName, true)
	s.Actual = map[string]any{"listed": err == nil, "entry": entryActual(listed)}
	s.finish(start, err)
	result.addStep(s)

	s = result.newStep("read", fileName)
	s.Input = map[string]any{"id": entry.ID, "offset": 0, "size": size}
	s.Expected = map[string]any{"bytes": size, "content_match": true}
	start = time.Now()
	data, readErr := readDriverEntry(stepContext(ctx, s), d, entry, size)
	if readErr == nil && !bytes.Equal(data, content) {
		readErr = fmt.Errorf("content mismatch: got %d bytes, want %d", len(data), len(content))
	}
	s.Actual = map[string]any{"bytes": len(data), "content_match": readErr == nil}
	s.finish(start, readErr)
	result.addStep(s)

	result.cleanupEntry(ctx, fx, "file", entry)
	result.cleanupEntry(ctx, fx, "test_dir", fx.TestDir)

	s = result.newStep("verify_cleanup_list", fx.Name())
	s.Input = map[string]any{"parent_id": fx.RootID(), "test_prefix": fx.Name()}
	s.Expected = map[string]any{"residual_count": 0}
	start = time.Now()
	residual, timeline, err := fx.ScanResidual(stepContext(ctx, s))
	result.ResidualTimeline = timeline
	for _, entry := range residual {
		result.Residual = append(result.Residual, artifactFromEntry("residual", entry, "matches test prefix after cleanup"))
	}
	s.Actual = map[string]any{"residual_count": len(residual), "residual": residualNames(residual)}
	s.finish(start, err)
	result.addStep(s)
	return result
}

// pseudoRandomBytes generates a deterministic, pseudo-random payload via a
// xorshift generator. Determinism keeps test failures reproducible; the
// seed makes the content effectively unique per test run.
func pseudoRandomBytes(seed uint64, size int) []byte {
	buf := make([]byte, size)
	x := seed | 1
	for i := range buf {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		buf[i] = byte(x)
	}
	return buf
}
