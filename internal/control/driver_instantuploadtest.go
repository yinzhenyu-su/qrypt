package control

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// instantUploadCountKeys lists the DebugSnapshot Extra keys accepted for
// service-side uploads. New drivers should report DebugExtraInstantUploadCount;
// the legacy rapid key is accepted so older snapshots remain readable.
var instantUploadCountKeys = []string{drive.DebugExtraInstantUploadCount, drive.DebugExtraLegacyRapidUploadCount}

func readInstantUploadCount(snap *drive.DebugSnapshot) (int64, bool) {
	for _, key := range instantUploadCountKeys {
		v, ok := snap.Extra[key]
		if !ok {
			continue
		}
		switch n := v.(type) {
		case int64:
			return n, true
		case float64:
			return int64(n), true
		}
	}
	return 0, false
}

// RunDriverInstantUploadTest uploads identical content twice and verifies that the
// second upload triggers the driver's service-side upload path (zero data
// transfer) by checking the driver's DebugSnapshot counter.
//
// Only drivers that implement SourceUploader, Writer, and Debugger can be
// fully verified. Drivers missing the Debugger interface are checked for
// basic upload success only (the counter cannot be inspected).
func RunDriverInstantUploadTest(ctx context.Context, mount string, d drive.Driver) *CRUDTestResult {
	result := &CRUDTestResult{
		OpID:         newDebugOperationID("instantupload"),
		Mount:        mount,
		Started:      time.Now(),
		Steps:        make([]CRUDTestStep, 0, 6),
		RetryCommand: fmt.Sprintf("qrypt debug test instantupload --mount %s --socket PATH", mount),
	}
	defer func() {
		result.Finished = time.Now()
		result.Duration = result.Finished.Sub(result.Started).String()
		for i := range result.Steps {
			if result.Steps[i].OpID == "" {
				result.Steps[i].OpID = result.OpID
			}
			if result.Steps[i].Mount == "" {
				result.Steps[i].Mount = result.Mount
			}
			if result.Steps[i].Driver == "" {
				result.Steps[i].Driver = result.Driver
			}
		}
	}()

	_, isUploader := d.(drive.SourceUploader)
	if !isUploader {
		result.Steps = append(result.Steps, CRUDTestStep{
			Operation:     "instant_upload",
			OK:            false,
			Error:         "driver does not implement SourceUploader",
			ErrorCategory: drive.ErrorCategoryUnsupported,
			Duration:      "0s",
		})
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	uploader := d.(drive.SourceUploader)

	writer, ok := d.(drive.Writer)
	if !ok {
		result.Steps = append(result.Steps, CRUDTestStep{
			Operation:     "instant_upload",
			OK:            false,
			Error:         "driver does not implement Writer",
			ErrorCategory: drive.ErrorCategoryUnsupported,
			Duration:      "0s",
		})
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	debugger, hasDebug := d.(drive.Debugger)
	if hasDebug {
		if snap, err := debugger.DebugSnapshot(ctx); err == nil {
			result.Driver = snap.Driver
		}
	}

	// Generate a unique test directory name.
	testSuffix := make([]byte, 6)
	if _, err := rand.Read(testSuffix); err != nil {
		result.Steps = append(result.Steps, CRUDTestStep{
			Operation:     "instant_upload",
			OK:            false,
			Error:         fmt.Sprintf("rand read: %v", err),
			ErrorCategory: drive.ErrorCategory(err),
			Duration:      "0s",
		})
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	testName := fmt.Sprintf("__qrypt_instant_upload_test_%x", testSuffix)

	rootID := driverProbeRootID(ctx, d)

	// 1. Mkdir test directory.
	s := stepOp("mkdir", testName)
	start := time.Now()
	testDir, err := writer.Mkdir(ctx, rootID, testName)
	s.finish(start, err)
	result.Steps = append(result.Steps, s)
	if err != nil {
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// 2. First upload — creates the file on the backend.
	const testContent = "qrypt-instant-upload-test-content-2024"
	s = stepOp("put", "original.bin")
	start = time.Now()
	firstSource := drive.NewBytesReadOnlyFileSource([]byte(testContent))
	_, err = uploader.PutSource(ctx, drive.UploadRequest{ParentID: testDir.ID, Name: "original.bin", Source: firstSource})
	s.finish(start, err)
	result.Steps = append(result.Steps, s)
	if err != nil {
		cleanupProbeDir(ctx, writer, testDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// 3. Snapshot the instant-upload counter before the second upload.
	var countBefore int64
	var canVerify bool
	if hasDebug {
		if snap, snapErr := debugger.DebugSnapshot(ctx); snapErr == nil {
			if c, ok := readInstantUploadCount(&snap); ok {
				countBefore = c
				canVerify = true
			}
		} else {
			cleanupProbeDir(ctx, writer, testDir)
			result.Steps = append(result.Steps, CRUDTestStep{
				Operation:     "verify_instant",
				OK:            false,
				Error:         fmt.Sprintf("debug snapshot before duplicate upload: %v", snapErr),
				ErrorCategory: drive.ErrorCategory(snapErr),
				Duration:      "0s",
			})
			result.Pass = false
			result.Finished = time.Now()
			return result
		}
	}

	// 4. Second upload of identical content should trigger service-side upload.
	s = stepOp("put_dup", "duplicate.bin")
	start = time.Now()
	secondSource := drive.NewBytesReadOnlyFileSource([]byte(testContent))
	_, err = uploader.PutSource(ctx, drive.UploadRequest{ParentID: testDir.ID, Name: "duplicate.bin", Source: secondSource})
	s.finish(start, err)
	result.Steps = append(result.Steps, s)
	if err != nil {
		cleanupProbeDir(ctx, writer, testDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// 5. Verify that the instant-upload counter increased.
	s = stepOp("verify_instant", "")
	start = time.Now()
	if hasDebug && !canVerify {
		err = fmt.Errorf("instant upload counter not reported by DebugSnapshot")
	} else if canVerify {
		if snap, snapErr := debugger.DebugSnapshot(ctx); snapErr == nil {
			if countAfter, ok := readInstantUploadCount(&snap); ok {
				if countAfter <= countBefore {
					err = fmt.Errorf("instant upload count did not increase: before=%d after=%d", countBefore, countAfter)
				}
			} else {
				err = fmt.Errorf("instant upload counter disappeared from Extra after being present")
			}
		} else {
			err = fmt.Errorf("debug snapshot: %w", snapErr)
		}
	}
	// When the driver has no Debugger, the counter cannot be inspected; both
	// uploads succeeding is the strongest check this generic test can make.
	s.finish(start, err)
	result.Steps = append(result.Steps, s)

	// 6. Remove the test directory.
	s = stepOp("rmdir", testName)
	start = time.Now()
	cleanupProbeDir(ctx, writer, testDir)
	s.finish(start, nil) // best-effort
	result.Steps = append(result.Steps, s)

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
