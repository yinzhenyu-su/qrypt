package drive

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"
)

// secUploadCountKeys is the set of Extra keys that drivers use to report
// rapid/instant upload statistics through their DebugSnapshot.
var secUploadCountKeys = []string{"rapid_upload_count", "instant_upload_count"}

func readSecUploadCount(snap *DebugSnapshot) (int64, bool) {
	for _, key := range secUploadCountKeys {
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

// RunRapidUploadTest uploads identical content twice and verifies that the
// second upload triggers the driver's rapid/instant upload path (zero data
// transfer) by checking the driver's DebugSnapshot counter.
//
// Only drivers that implement SourceUploader, Writer, and Debugger can be
// fully verified. Drivers missing the Debugger interface are checked for
// basic upload success only (the counter cannot be inspected).
func RunRapidUploadTest(ctx context.Context, mount string, d Driver) *CRUDTestResult {
	result := &CRUDTestResult{
		Mount:   mount,
		Started: time.Now(),
		Steps:   make([]CRUDTestStep, 0, 6),
	}

	_, isUploader := d.(SourceUploader)
	if !isUploader {
		result.Steps = append(result.Steps, CRUDTestStep{
			Operation: "rapid_upload",
			OK:        false,
			Error:     "driver does not implement SourceUploader",
			Duration:  "0s",
		})
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	uploader := d.(SourceUploader)

	writer, ok := d.(Writer)
	if !ok {
		result.Steps = append(result.Steps, CRUDTestStep{
			Operation: "rapid_upload",
			OK:        false,
			Error:     "driver does not implement Writer",
			Duration:  "0s",
		})
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	debugger, hasDebug := d.(Debugger)
	if hasDebug {
		if snap, err := debugger.DebugSnapshot(ctx); err == nil {
			result.Driver = snap.Driver
		}
	}

	// Generate a unique test directory name.
	testSuffix := make([]byte, 6)
	if _, err := rand.Read(testSuffix); err != nil {
		result.Steps = append(result.Steps, CRUDTestStep{
			Operation: "rapid_upload",
			OK:        false,
			Error:     fmt.Sprintf("rand read: %v", err),
			Duration:  "0s",
		})
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	testName := fmt.Sprintf("__qrypt_rapid_test_%x", testSuffix)

	rootID := testRootID(ctx, d)

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
	const testContent = "qrypt-rapid-upload-test-content-2024"
	s = stepOp("put", "original.bin")
	start = time.Now()
	firstSource := NewBytesReadOnlyFileSource([]byte(testContent))
	_, err = uploader.PutSource(ctx, UploadRequest{ParentID: testDir.ID, Name: "original.bin", Source: firstSource})
	s.finish(start, err)
	result.Steps = append(result.Steps, s)
	if err != nil {
		cleanup(ctx, writer, testDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// 3. Snapshot the rapid-upload counter before the second upload.
	var countBefore int64
	var canVerify bool
	if hasDebug {
		if snap, snapErr := debugger.DebugSnapshot(ctx); snapErr == nil {
			if c, ok := readSecUploadCount(&snap); ok {
				countBefore = c
				canVerify = true
			}
		} else {
			cleanup(ctx, writer, testDir)
			result.Steps = append(result.Steps, CRUDTestStep{
				Operation: "verify_rapid",
				OK:        false,
				Error:     fmt.Sprintf("debug snapshot before duplicate upload: %v", snapErr),
				Duration:  "0s",
			})
			result.Pass = false
			result.Finished = time.Now()
			return result
		}
	}

	// 4. Second upload of identical content — should trigger rapid/instant upload.
	s = stepOp("put_dup", "duplicate.bin")
	start = time.Now()
	secondSource := NewBytesReadOnlyFileSource([]byte(testContent))
	_, err = uploader.PutSource(ctx, UploadRequest{ParentID: testDir.ID, Name: "duplicate.bin", Source: secondSource})
	s.finish(start, err)
	result.Steps = append(result.Steps, s)
	if err != nil {
		cleanup(ctx, writer, testDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// 5. Verify that the rapid-upload counter increased.
	s = stepOp("verify_rapid", "")
	start = time.Now()
	if hasDebug && !canVerify {
		err = fmt.Errorf("rapid upload counter not reported by DebugSnapshot")
	} else if canVerify {
		if snap, snapErr := debugger.DebugSnapshot(ctx); snapErr == nil {
			if countAfter, ok := readSecUploadCount(&snap); ok {
				if countAfter <= countBefore {
					err = fmt.Errorf("rapid upload count did not increase: before=%d after=%d", countBefore, countAfter)
				}
			} else {
				err = fmt.Errorf("rapid upload counter disappeared from Extra after being present")
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
	_ = writer.Remove(ctx, testDir)
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

func testRootID(ctx context.Context, d Driver) string {
	if resolver, ok := d.(PathResolver); ok {
		if rootID, err := resolver.ResolvePath(ctx, "/"); err == nil && rootID != "" {
			return rootID
		}
	}
	entries, err := d.List(ctx, "")
	if err == nil && len(entries) > 0 && entries[0].ParentID != "" {
		return entries[0].ParentID
	}
	return "root"
}
