package contracttest

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	// DefaultUploadProbeSize is large enough to span several backend chunks
	// for drivers that upload in 16 MiB parts.
	DefaultUploadProbeSize = 64 << 20
	// MaxUploadProbeSize bounds one upload probe; large enough to exercise
	// drivers whose timeout only triggers on multi-gigabyte files.
	MaxUploadProbeSize = 64 << 30
	// uploadFullVerifyLimit is the largest payload read back byte-for-byte.
	// Bigger payloads are verified with sampled ranges so a probe never
	// downloads the whole file.
	uploadFullVerifyLimit = 64 << 20
	uploadVerifyChunk     = 1 << 20
)

// Upload fault kinds accepted by RunDriverUploadFaultTest.
const (
	UploadFaultStall    = "stall"
	UploadFaultTruncate = "truncate"
	UploadFaultNotFound = "notfound"
	// uploadFaultTimeout bounds a stalled upload from the probe side. A driver
	// with a working watchdog or request timeout fails well before this.
	uploadFaultTimeout = 3 * time.Minute
)

// UploadMeasurement summarises one completed upload.
type UploadMeasurement struct {
	Bytes         int64  `json:"bytes"`
	DurationUS    int64  `json:"duration_us"`
	ThroughputBPS int64  `json:"end_to_end_bps"`
	VerifyMode    string `json:"verify_mode"`
	VerifiedBytes int64  `json:"verified_bytes"`
}

// UploadTestDetails carries upload-specific results outside the unified step
// trace.
type UploadTestDetails struct {
	OpID         string              `json:"op_id"`
	Measurements []UploadMeasurement `json:"measurements,omitempty"`
	Fault        string              `json:"fault,omitempty"`
	FaultOutcome string              `json:"fault_outcome,omitempty"`
}

// UploadTestResult is a CRUD-shaped result plus upload timing measurements.
type UploadTestResult struct {
	CRUDTestResult
	Upload *UploadTestDetails `json:"upload,omitempty"`
}

// RunDriverUploadTest uploads one generated file of the requested size,
// verifies the remote size and content, and removes the fixture. It measures
// the full PutSource call, so it also proves that a slow upload survives the
// driver's request timeouts instead of restarting from zero.
func RunDriverUploadTest(ctx context.Context, mount string, d drive.Driver, size int64) *UploadTestResult {
	return runDriverUploadTest(ctx, mount, d, size, uploadProbeConfig{})
}

// RunDriverUploadFaultTest exercises one upload failure path instead of a
// successful upload: a stalled body, a truncated body, or a parent that no
// longer exists. It is the deterministic counterpart to waiting for a real
// network stall.
func RunDriverUploadFaultTest(ctx context.Context, mount string, d drive.Driver, size int64, fault string, after int64) *UploadTestResult {
	return runDriverUploadTest(ctx, mount, d, size, uploadProbeConfig{fault: fault, faultAfter: after})
}

type uploadProbeConfig struct {
	fault      string
	faultAfter int64
}

func runDriverUploadTest(ctx context.Context, mount string, d drive.Driver, size int64, cfg uploadProbeConfig) *UploadTestResult {
	opID := newDebugOperationID("upload")
	result := &UploadTestResult{
		CRUDTestResult: CRUDTestResult{
			OpID:         opID,
			Mount:        mount,
			Started:      time.Now(),
			Steps:        make([]CRUDTestStep, 0, 7),
			RetryCommand: fmt.Sprintf("qrypt probe test upload --mount %s --size %d", mount, size),
		},
		Upload: &UploadTestDetails{OpID: opID},
	}
	if size <= 0 {
		size = DefaultUploadProbeSize
	}
	if size > MaxUploadProbeSize {
		result.addStep(CRUDTestStep{
			Operation:     "upload",
			OpID:          opID,
			Mount:         mount,
			OK:            false,
			Error:         fmt.Sprintf("upload probe size %d exceeds the %d-byte limit", size, int64(MaxUploadProbeSize)),
			ErrorCategory: drive.ErrorCategoryInvalidRequest,
			Duration:      "0s",
		})
		result.Pass = false
		return result
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
			Operation:     "upload",
			OpID:          opID,
			Mount:         mount,
			OK:            false,
			Error:         "driver does not implement Writer (read-only)",
			ErrorCategory: drive.ErrorCategoryUnsupported,
			Duration:      "0s",
		})
		return result
	}
	if !drive.HasCapability(d, drive.CapabilitySourceUploader) {
		result.addStep(CRUDTestStep{
			Operation:     "upload",
			OpID:          opID,
			Mount:         mount,
			OK:            false,
			Error:         "driver does not implement SourceUploader",
			ErrorCategory: drive.ErrorCategoryUnsupported,
			Duration:      "0s",
		})
		return result
	}

	s := result.newStep("mkdir", "test-dir")
	start := time.Now()
	fx, err := NewFixture(ctx, d, "upload")
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

	if cfg.fault != "" {
		result.Upload.Fault = cfg.fault
		runFaultedUpload(ctx, result, fx, d, size, cfg)
		if cfg.fault != UploadFaultNotFound {
			result.cleanupEntry(ctx, fx, "test_dir", fx.TestDir)
		}
		appendUploadResidualStep(ctx, result, fx)
		return result
	}

	const fileName = "payload.txt"
	source := newPatternFileSource(size)
	s = result.newStep("put", fileName)
	s.Input = map[string]any{"parent_id": fx.TestDir.ID, "name": fileName, "bytes": size}
	s.Expected = map[string]any{"name": fileName, "size": size, "is_dir": false}
	start = time.Now()
	entry, err := d.PutSource(stepContext(ctx, s), drive.UploadRequest{
		ParentID: fx.TestDir.ID,
		Name:     fileName,
		Source:   source,
	})
	putDuration := time.Since(start)
	s.Actual = entryActual(entry)
	s.finish(start, err)
	result.addStep(s)
	if err != nil {
		return result
	}
	result.addCreated("file", entry)

	measurement := UploadMeasurement{
		Bytes:         size,
		DurationUS:    int64(putDuration / time.Microsecond),
		ThroughputBPS: bytesPerSecond(size, putDuration),
	}
	result.Upload.Measurements = append(result.Upload.Measurements, measurement)

	s = result.newStep("verify_put_list", fileName)
	s.Input = map[string]any{"parent_id": fx.TestDir.ID, "name": fileName}
	s.Expected = map[string]any{"listed": true, "size": size}
	start = time.Now()
	listed, err := fx.VerifyList(stepContext(ctx, s), fx.TestDir.ID, fileName, true)
	s.Actual = map[string]any{"listed": err == nil, "entry": entryActual(listed)}
	s.finish(start, err)
	result.addStep(s)

	s = result.newStep("verify_content", fileName)
	start = time.Now()
	verified, mode, verifyErr := verifyUploadedContent(stepContext(ctx, s), d, entry, size)
	s.Input = map[string]any{"size": size}
	s.Expected = map[string]any{"content_match": true}
	s.Actual = map[string]any{"bytes": verified, "mode": mode, "content_match": verifyErr == nil}
	s.finish(start, verifyErr)
	result.addStep(s)
	result.Upload.Measurements[0].VerifyMode = mode
	result.Upload.Measurements[0].VerifiedBytes = verified

	result.cleanupEntry(ctx, fx, "file", entry)
	result.cleanupEntry(ctx, fx, "test_dir", fx.TestDir)
	appendUploadResidualStep(ctx, result, fx)
	return result
}

// appendUploadResidualStep scans the fixture prefix for leftover objects
// after cleanup and records the unified residual step.
func appendUploadResidualStep(ctx context.Context, result *UploadTestResult, fx *Fixture) {
	s := result.newStep("verify_cleanup_list", fx.Name())
	s.Input = map[string]any{"parent_id": fx.RootID(), "test_prefix": fx.Name()}
	s.Expected = map[string]any{"residual_count": 0}
	start := time.Now()
	residual, timeline, err := fx.ScanResidual(stepContext(ctx, s))
	result.ResidualTimeline = timeline
	for _, entry := range residual {
		result.Residual = append(result.Residual, artifactFromEntry("residual", entry, "matches test prefix after cleanup"))
	}
	s.Actual = map[string]any{"residual_count": len(residual), "residual": residualNames(residual)}
	s.finish(start, err)
	result.addStep(s)
}

// runFaultedUpload performs one upload that is expected to fail and records
// whether the driver surfaced the failure itself instead of relying on the
// probe deadline.
func runFaultedUpload(ctx context.Context, result *UploadTestResult, fx *Fixture, d drive.Driver, size int64, cfg uploadProbeConfig) {
	if cfg.faultAfter <= 0 {
		cfg.faultAfter = 1 << 20
	}
	parentID := fx.TestDir.ID
	source := drive.ReadOnlyFileSource(newPatternFileSource(size))
	switch cfg.fault {
	case UploadFaultStall:
		source = &faultFileSource{inner: source, after: cfg.faultAfter}
	case UploadFaultTruncate:
		source = &faultFileSource{inner: source, after: cfg.faultAfter, truncate: true}
	case UploadFaultNotFound:
		// Remove the intended parent so the driver must report a missing
		// target. Retrying cannot fix this; the VFS layer treats not-found as
		// a permanent upload failure.
		result.cleanupEntry(ctx, fx, "test_dir", fx.TestDir)
	default:
		result.addStep(CRUDTestStep{
			Operation:     "put_fault",
			OpID:          result.OpID,
			Mount:         result.Mount,
			OK:            false,
			Error:         fmt.Sprintf("unknown upload fault %q (want %s, %s, or %s)", cfg.fault, UploadFaultStall, UploadFaultTruncate, UploadFaultNotFound),
			ErrorCategory: drive.ErrorCategoryInvalidRequest,
			Duration:      "0s",
		})
		return
	}

	putCtx := ctx
	cancel := func() {}
	if cfg.fault == UploadFaultStall {
		putCtx, cancel = context.WithTimeout(ctx, uploadFaultTimeout)
	}
	defer cancel()

	s := result.newStep("put_fault", cfg.fault)
	s.Input = map[string]any{"fault": cfg.fault, "after_bytes": cfg.faultAfter, "bytes": size}
	s.Expected = map[string]any{"error": true}
	start := time.Now()
	entry, err := d.PutSource(stepContext(putCtx, s), drive.UploadRequest{
		ParentID: parentID,
		Name:     "payload.txt",
		Source:   source,
	})
	duration := time.Since(start)
	s.Duration = duration.String()
	s.DurationMS = DurationMillis(duration)
	s.Actual = map[string]any{
		"error":          errorText(err),
		"category":       drive.ErrorCategory(err),
		"duration_ms":    s.DurationMS,
		"uploaded_bytes": entry.Size,
	}
	switch {
	case err == nil:
		s.OK = false
		s.Error = "fault did not surface an upload error"
		s.ErrorCategory = ""
	case cfg.fault == UploadFaultStall && putCtx.Err() != nil:
		// Our own deadline fired first: the driver has no idle watchdog or
		// request timeout that catches a body which stops moving.
		s.OK = false
		s.Error = fmt.Sprintf("driver did not cancel the stalled upload within %s", uploadFaultTimeout)
		result.Upload.FaultOutcome = "probe_deadline"
	default:
		s.OK = true
		result.Upload.FaultOutcome = "driver"
	}
	result.addStep(s)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// faultFileSource wraps a source to stall or truncate its body after a byte
// threshold, emulating a provider that stops reading or a connection that
// closes early.
type faultFileSource struct {
	inner    drive.ReadOnlyFileSource
	after    int64
	truncate bool
}

func (s *faultFileSource) Size() int64 { return s.inner.Size() }

func (s *faultFileSource) Open(ctx context.Context) (drive.ReadOnlyFile, error) {
	inner, err := s.inner.Open(ctx)
	if err != nil {
		return nil, err
	}
	return &faultFile{
		inner:    inner,
		after:    s.after,
		truncate: s.truncate,
		done:     ctx.Done(),
	}, nil
}

type faultFile struct {
	inner    drive.ReadOnlyFile
	after    int64
	truncate bool
	offset   int64
	done     <-chan struct{}
}

func (f *faultFile) Read(p []byte) (int, error) {
	if f.offset >= f.after {
		return f.fail()
	}
	if remaining := f.after - f.offset; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := f.inner.Read(p)
	f.offset += int64(n)
	return n, err
}

func (f *faultFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= f.after {
		return f.fail()
	}
	if remaining := f.after - off; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	return f.inner.ReadAt(p, off)
}

func (f *faultFile) Seek(offset int64, whence int) (int64, error) {
	position, err := f.inner.Seek(offset, whence)
	if err != nil {
		return position, err
	}
	f.offset = position
	return position, nil
}

func (f *faultFile) Close() error { return f.inner.Close() }

func (f *faultFile) fail() (int, error) {
	if f.truncate {
		return 0, io.ErrUnexpectedEOF
	}
	<-f.done
	return 0, context.Canceled
}

// verifyUploadedContent checks the returned size and compares either the whole
// payload (small files) or sampled ranges (large files) against the generated
// pattern.
func verifyUploadedContent(ctx context.Context, d drive.Driver, entry drive.Entry, size int64) (int64, string, error) {
	if entry.Size != size {
		return 0, "", fmt.Errorf("uploaded size = %d, want %d", entry.Size, size)
	}
	if size <= uploadFullVerifyLimit {
		data, err := readDriverEntry(ctx, d, entry, size)
		if err != nil {
			return 0, "full", err
		}
		if int64(len(data)) != size {
			return 0, "full", fmt.Errorf("read back %d bytes, want %d", len(data), size)
		}
		if err := comparePattern(data, 0); err != nil {
			return 0, "full", err
		}
		return size, "full", nil
	}
	offsets := []int64{0, size / 2, size - uploadVerifyChunk}
	if offsets[2] < 0 {
		offsets[2] = 0
	}
	var verified int64
	for _, offset := range offsets {
		length := int64(uploadVerifyChunk)
		if offset+length > size {
			length = size - offset
		}
		rc, err := d.Read(ctx, entry, offset, length)
		if err != nil {
			return verified, "sample", fmt.Errorf("read sample at %d: %w", offset, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, length))
		rc.Close()
		if readErr != nil {
			return verified, "sample", fmt.Errorf("read sample at %d: %w", offset, readErr)
		}
		if int64(len(data)) != length {
			return verified, "sample", fmt.Errorf("sample at %d returned %d bytes, want %d", offset, len(data), length)
		}
		if err := comparePattern(data, offset); err != nil {
			return verified, "sample", err
		}
		verified += length
	}
	return verified, "sample", nil
}

func comparePattern(data []byte, offset int64) error {
	for i, got := range data {
		want := patternByte(offset + int64(i))
		if got != want {
			return fmt.Errorf("content mismatch at offset %d: got %d, want %d", offset+int64(i), got, want)
		}
	}
	return nil
}

// patternByte is the deterministic payload byte at an absolute offset. It
// depends only on the offset, so seeks, re-reads, and hash passes see the same
// content without storing the payload.
func patternByte(offset int64) byte {
	return byte(offset)
}

// patternFileSource streams a deterministic pattern instead of allocating the
// payload, which keeps multi-gigabyte upload probes cheap. Content hashes are
// computed once on demand because encrypted dedup mounts require source
// metadata before a driver will accept the upload.
type patternFileSource struct {
	size   int64
	seed   int64
	mu     sync.Mutex
	hashes map[drive.HashAlgorithm][]byte
}

func newPatternFileSource(size int64) *patternFileSource {
	return &patternFileSource{size: size}
}

// newSeededPatternFileSource makes the bytes unique per upload so providers
// with content deduplication cannot answer an upload from an earlier file.
func newSeededPatternFileSource(size, seed int64) *patternFileSource {
	return &patternFileSource{size: size, seed: seed}
}

func (s *patternFileSource) Size() int64 { return s.size }

func (s *patternFileSource) Open(ctx context.Context) (drive.ReadOnlyFile, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return &patternFile{size: s.size, seed: s.seed}, nil
}

// Hash implements drive.HashProvider. All three hashes are produced in one
// pass and cached, so repeated driver requests cost nothing extra.
func (s *patternFileSource) Hash(algorithm drive.HashAlgorithm) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hashes == nil {
		s.hashes = make(map[drive.HashAlgorithm][]byte, 3)
	}
	if sum, ok := s.hashes[algorithm]; ok {
		return append([]byte(nil), sum...), true
	}
	if len(s.hashes) == 0 {
		md5Hash := md5.New()
		sha1Hash := sha1.New()
		sha256Hash := sha256.New()
		buf := make([]byte, 1<<20)
		for offset := int64(0); offset < s.size; {
			n := int64(len(buf))
			if offset+n > s.size {
				n = s.size - offset
			}
			fillPattern(buf[:n], offset+s.seed)
			_, _ = md5Hash.Write(buf[:n])
			_, _ = sha1Hash.Write(buf[:n])
			_, _ = sha256Hash.Write(buf[:n])
			offset += n
		}
		s.hashes[drive.HashMD5] = md5Hash.Sum(nil)
		s.hashes[drive.HashSHA1] = sha1Hash.Sum(nil)
		s.hashes[drive.HashSHA256] = sha256Hash.Sum(nil)
	}
	sum, ok := s.hashes[algorithm]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), sum...), true
}

type patternFile struct {
	size   int64
	seed   int64
	offset int64
}

func (f *patternFile) Read(p []byte) (int, error) {
	if f.offset >= f.size {
		return 0, io.EOF
	}
	n := int64(len(p))
	if f.offset+n > f.size {
		n = f.size - f.offset
	}
	fillPattern(p[:n], f.offset+f.seed)
	f.offset += n
	return int(n), nil
}

func (f *patternFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= f.size {
		return 0, io.EOF
	}
	n := int64(len(p))
	if off+n > f.size {
		n = f.size - off
	}
	fillPattern(p[:n], off+f.seed)
	return int(n), nil
}

func (f *patternFile) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.offset + offset
	case io.SeekEnd:
		next = f.size + offset
	default:
		return 0, fmt.Errorf("pattern source: invalid whence %d", whence)
	}
	if next < 0 {
		return 0, fmt.Errorf("pattern source: negative offset %d", next)
	}
	f.offset = next
	return next, nil
}

func (f *patternFile) Close() error { return nil }

func fillPattern(p []byte, offset int64) {
	for i := range p {
		p[i] = patternByte(offset + int64(i))
	}
}

var _ drive.ReadOnlyFileSource = (*patternFileSource)(nil)
var _ drive.HashProvider = (*patternFileSource)(nil)
var _ drive.ReadOnlyFile = (*patternFile)(nil)
