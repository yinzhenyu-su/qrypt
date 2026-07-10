package drive

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"time"
)

// CRUDTestStep records one step of a CRUD test.
type CRUDTestStep struct {
	Operation string `json:"operation"`
	Name      string `json:"name,omitempty"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Duration  string `json:"duration"`
}

// CRUDTestResult aggregates the full CRUD test outcome for one driver.
type CRUDTestResult struct {
	Mount    string         `json:"mount"`
	Driver   string         `json:"driver,omitempty"`
	Pass     bool           `json:"pass"`
	Steps    []CRUDTestStep `json:"steps"`
	Started  time.Time      `json:"started_at"`
	Finished time.Time      `json:"finished_at"`
}

type crudTestCtx struct {
	mount  string
	ctx    context.Context
	driver Driver
	result *CRUDTestResult
	parent string
	writer Writer
}

func stepOp(op, name string) CRUDTestStep {
	return CRUDTestStep{Operation: op, Name: name, Duration: "0s"}
}

func (s *CRUDTestStep) done(err error) {
	if err != nil {
		s.OK = false
		s.Error = err.Error()
	} else {
		s.OK = true
	}
}

func (s *CRUDTestStep) finish(start time.Time, err error) {
	s.Duration = time.Since(start).String()
	s.done(err)
}

// RunCRUDTest executes a standard CRUD test sequence on the given driver.
// It creates a temporary directory, creates a file, reads it back, renames it,
// and cleans up. Each operation is evaluated by its own return value — no
// List-based verification is used.
func RunCRUDTest(ctx context.Context, mount string, d Driver) *CRUDTestResult {
	result := &CRUDTestResult{
		Mount:   mount,
		Started: time.Now(),
		Steps:   make([]CRUDTestStep, 0, 6),
	}

	tc := &crudTestCtx{
		mount:  mount,
		ctx:    ctx,
		driver: d,
		result: result,
	}
	if debugger, ok := d.(Debugger); ok {
		if snap, err := debugger.DebugSnapshot(ctx); err == nil {
			result.Driver = snap.Driver
		}
	}

	// Determine writer / uploader support.
	if !HasCapability(d, CapabilityWriter) {
		result.Steps = append(result.Steps, CRUDTestStep{
			Operation: "crud",
			OK:        false,
			Error:     "driver does not implement Writer (read-only)",
			Duration:  "0s",
		})
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	w := d.(Writer)
	tc.writer = w
	var uploader SourceUploader
	if HasCapability(d, CapabilitySourceUploader) {
		uploader = d.(SourceUploader)
	}

	// Generate a unique test directory name.
	testSuffix := make([]byte, 6)
	rand.Read(testSuffix)
	testName := fmt.Sprintf("__qrypt_test_%x", testSuffix)

	// Determine root parent ID. Try common sentinel values.
	rootID := ""
	entries, err := d.List(ctx, "")
	if err == nil && len(entries) > 0 {
		rootID = entries[0].ParentID
	}
	if rootID == "" {
		rootID = "root"
	}

	// 1. Mkdir test directory.
	s := stepOp("mkdir", testName)
	start := time.Now()
	testDir, err := w.Mkdir(ctx, rootID, testName)
	s.finish(start, err)
	result.Steps = append(result.Steps, s)
	if err != nil {
		result.Pass = false
		result.Finished = time.Now()
		return result
	}
	tc.parent = testDir.ID

	// 2. Create a test file.
	const testContent = "qrypt-crud-test-content-42"
	s = stepOp("put", "test.txt")
	start = time.Now()
	var fileEntry Entry
	if uploader != nil {
		fileEntry, err = uploader.PutSource(ctx, UploadRequest{
			ParentID: testDir.ID,
			Name:     "test.txt",
			Source:   NewBytesReadOnlyFileSource([]byte(testContent)),
		})
	} else {
		// Fallback: use WriteAt on the Writer if SourceUploader not available.
		fileEntry, err = w.Mkdir(ctx, testDir.ID, "test.txt")
		// Most drivers won't support Mkdir for files. Report unsupported.
		err = fmt.Errorf("uploader not implemented")
	}
	s.finish(start, err)
	result.Steps = append(result.Steps, s)
	if err != nil {
		cleanup(ctx, w, testDir)
		result.Pass = false
		result.Finished = time.Now()
		return result
	}

	// 3. Read back and verify content.
	s = stepOp("read", "test.txt")
	start = time.Now()
	rc, err := d.Read(ctx, fileEntry, 0, int64(len(testContent)))
	if err == nil {
		data, readErr := io.ReadAll(rc)
		rc.Close()
		if readErr != nil {
			err = readErr
		} else if string(data) != testContent {
			err = fmt.Errorf("content mismatch: got %q, want %q", string(data), testContent)
		}
	}
	s.finish(start, err)
	result.Steps = append(result.Steps, s)

	// 4. Rename the file.
	s = stepOp("rename", "renamed.txt")
	start = time.Now()
	err = w.Rename(ctx, fileEntry, "renamed.txt")
	s.finish(start, err)
	result.Steps = append(result.Steps, s)

	// 5. Remove the renamed file.
	s = stepOp("remove", "renamed.txt")
	start = time.Now()
	// After rename, the ID stays the same; remove by that ID.
	err = w.Remove(ctx, fileEntry)
	s.finish(start, err)
	result.Steps = append(result.Steps, s)

	// 6. Remove the test directory.
	s = stepOp("rmdir", testName)
	start = time.Now()
	err = w.Remove(ctx, testDir)
	s.finish(start, err)
	result.Steps = append(result.Steps, s)

	parentForVerify := testDir.ParentID
	if parentForVerify == "" {
		parentForVerify = rootID
	}
	s = stepOp("verify_list", parentForVerify)
	start = time.Now()
	err = verifyCleanList(ctx, d, parentForVerify, testName)
	s.finish(start, err)
	result.Steps = append(result.Steps, s)

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

func cleanup(ctx context.Context, w Writer, dir Entry) {
	_ = w.Remove(ctx, dir)
}

func verifyCleanList(ctx context.Context, d Driver, parentID string, testPrefix string) error {
	const maxAttempts = 3
	delay := 1 * time.Second

	var lastEntries []Entry
	for attempt := range maxAttempts {
		if attempt > 0 {
			time.Sleep(delay)
			delay *= 2
		}

		entries, err := d.List(ctx, parentID)
		if err != nil {
			return fmt.Errorf("verify list: %w", err)
		}
		lastEntries = entries

		found := false
		for _, e := range entries {
			if strings.HasPrefix(e.Name, testPrefix) {
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}

	names := make([]string, len(lastEntries))
	for i, e := range lastEntries {
		names[i] = e.Name
	}
	return fmt.Errorf("stale entries after %d list attempts: %v", maxAttempts, names)
}
