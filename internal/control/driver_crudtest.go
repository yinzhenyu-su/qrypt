package control

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// CRUDTestStep records one step of a CRUD test.
type CRUDTestStep struct {
	Operation     string         `json:"operation"`
	Name          string         `json:"name,omitempty"`
	OpID          string         `json:"op_id,omitempty"`
	Mount         string         `json:"mount,omitempty"`
	Driver        string         `json:"driver,omitempty"`
	OK            bool           `json:"ok"`
	Error         string         `json:"error,omitempty"`
	ErrorCategory string         `json:"error_category,omitempty"`
	Duration      string         `json:"duration"`
	DurationMS    int64          `json:"duration_ms"`
	Input         map[string]any `json:"input,omitempty"`
	Expected      map[string]any `json:"expected,omitempty"`
	Actual        map[string]any `json:"actual,omitempty"`
}

// CRUDTestResult aggregates the full CRUD test outcome for one driver.
type CRUDTestResult struct {
	OpID             string                 `json:"op_id"`
	Mount            string                 `json:"mount"`
	Driver           string                 `json:"driver,omitempty"`
	Pass             bool                   `json:"pass"`
	Steps            []CRUDTestStep         `json:"steps"`
	Created          []CRUDTestArtifact     `json:"created,omitempty"`
	Cleanup          []CRUDCleanupResult    `json:"cleanup,omitempty"`
	Residual         []CRUDTestArtifact     `json:"residual,omitempty"`
	ResidualTimeline []CRUDVisibilitySample `json:"residual_timeline,omitempty"`
	Metrics          []drive.MetricEvent    `json:"metrics,omitempty"`
	RetryCommand     string                 `json:"retry_command,omitempty"`
	CleanupGuidance  string                 `json:"cleanup_guidance,omitempty"`
	Started          time.Time              `json:"started_at"`
	Finished         time.Time              `json:"finished_at"`
	Duration         string                 `json:"duration"`
	DurationMS       int64                  `json:"duration_ms"`
}

type CRUDTestArtifact struct {
	Role     string `json:"role"`
	Name     string `json:"name"`
	ID       string `json:"id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type CRUDCleanupResult struct {
	Operation     string `json:"operation"`
	Role          string `json:"role,omitempty"`
	Name          string `json:"name,omitempty"`
	ID            string `json:"id,omitempty"`
	OK            bool   `json:"ok"`
	Error         string `json:"error,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
	Duration      string `json:"duration"`
	DurationMS    int64  `json:"duration_ms"`
}

type CRUDVisibilitySample struct {
	Attempt       int      `json:"attempt"`
	Elapsed       string   `json:"elapsed"`
	ElapsedMS     int64    `json:"elapsed_ms"`
	ResidualCount int      `json:"residual_count"`
	ResidualNames []string `json:"residual_names,omitempty"`
	Error         string   `json:"error,omitempty"`
}

func stepOp(op, name string) CRUDTestStep {
	return CRUDTestStep{Operation: op, Name: name, Duration: "0s"}
}

func (s *CRUDTestStep) done(err error) {
	if err != nil {
		s.OK = false
		s.Error = err.Error()
		s.ErrorCategory = drive.ErrorCategory(err)
	} else {
		s.OK = true
	}
}

func (s *CRUDTestStep) finish(start time.Time, err error) {
	duration := time.Since(start)
	s.Duration = duration.String()
	s.DurationMS = durationMillis(duration)
	s.done(err)
}

func (r *CRUDTestResult) newStep(op, name string) CRUDTestStep {
	step := stepOp(op, name)
	step.OpID = r.OpID
	step.Mount = r.Mount
	step.Driver = r.Driver
	return step
}

func (r *CRUDTestResult) addStep(step CRUDTestStep) {
	if step.OpID == "" {
		step.OpID = r.OpID
	}
	if step.Mount == "" {
		step.Mount = r.Mount
	}
	if step.Driver == "" {
		step.Driver = r.Driver
	}
	r.Steps = append(r.Steps, step)
}

func (r *CRUDTestResult) addCreated(role string, entry drive.Entry) {
	r.Created = append(r.Created, artifactFromEntry(role, entry, ""))
}

func artifactFromEntry(role string, entry drive.Entry, reason string) CRUDTestArtifact {
	return CRUDTestArtifact{
		Role:     role,
		Name:     entry.Name,
		ID:       entry.ID,
		ParentID: entry.ParentID,
		IsDir:    entry.IsDir,
		Size:     entry.Size,
		Reason:   reason,
	}
}

// RunDriverCRUDTest executes a contract-oriented CRUD test sequence on the
// given driver. It creates a temporary directory, checks list visibility,
// uploads a small matrix of file names and sizes, reads content back, renames a
// file, removes all created objects, and reports cleanup/residual state.
func RunDriverCRUDTest(ctx context.Context, mount string, d drive.Driver) *CRUDTestResult {
	result := &CRUDTestResult{
		OpID:         newDebugOperationID("crud"),
		Mount:        mount,
		Started:      time.Now(),
		Steps:        make([]CRUDTestStep, 0, 24),
		RetryCommand: fmt.Sprintf("qrypt debug test crud --mount %s --socket PATH", mount),
	}
	defer func() {
		result.Finished = time.Now()
		duration := result.Finished.Sub(result.Started)
		result.Duration = duration.String()
		result.DurationMS = durationMillis(duration)
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

	// Determine writer / uploader support.
	if !drive.HasCapability(d, drive.CapabilityWriter) {
		result.addStep(CRUDTestStep{
			Operation:     "crud",
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
			Operation:     "capability_check",
			OpID:          result.OpID,
			Mount:         result.Mount,
			Driver:        result.Driver,
			OK:            false,
			Error:         "driver does not implement SourceUploader",
			ErrorCategory: drive.ErrorCategoryUnsupported,
			Duration:      "0s",
			Expected:      map[string]any{"source_uploader": true},
			Actual:        map[string]any{"source_uploader": false},
		})
		return result
	}

	// Generate a unique test directory name.
	testSuffix := make([]byte, 6)
	if _, err := rand.Read(testSuffix); err != nil {
		result.addStep(CRUDTestStep{
			Operation: "generate_name",
			OpID:      result.OpID,
			Mount:     result.Mount,
			Driver:    result.Driver,
			OK:        false,
			Error:     err.Error(),
			Duration:  "0s",
		})
		return result
	}
	testName := fmt.Sprintf("__qrypt_test_%x", testSuffix)

	rootID := driverProbeRootID(ctx, d)

	// 1. Mkdir test directory.
	s := result.newStep("mkdir", testName)
	s.Input = map[string]any{"parent_id": rootID, "name": testName}
	s.Expected = map[string]any{"is_dir": true, "name": testName}
	start := time.Now()
	testDir, err := d.Mkdir(stepContext(ctx, s), rootID, testName)
	s.Actual = entryActual(testDir)
	s.finish(start, err)
	result.addStep(s)
	if err != nil {
		return result
	}
	result.addCreated("test_dir", testDir)

	s = result.newStep("verify_mkdir_list", testName)
	s.Input = map[string]any{"parent_id": rootID, "name": testName}
	s.Expected = map[string]any{"listed": true}
	start = time.Now()
	listedDir, err := verifyListContains(stepContext(ctx, s), d, rootID, testName)
	s.Actual = map[string]any{"listed": err == nil, "entry": entryActual(listedDir)}
	s.finish(start, err)
	result.addStep(s)

	nestedName := "nested dir"
	s = result.newStep("mkdir_nested", nestedName)
	s.Input = map[string]any{"parent_id": testDir.ID, "name": nestedName}
	s.Expected = map[string]any{"is_dir": true, "name": nestedName}
	start = time.Now()
	nestedDir, err := d.Mkdir(stepContext(ctx, s), testDir.ID, nestedName)
	s.Actual = entryActual(nestedDir)
	s.finish(start, err)
	result.addStep(s)
	if err == nil {
		result.addCreated("nested_dir", nestedDir)
	}

	fileCases := []crudFileCase{
		{Name: "test.txt", Data: []byte("qrypt-crud-test-content-42"), Parent: testDir, Role: "primary_file"},
		{Name: "empty.bin", Data: []byte{}, Parent: testDir, Role: "empty_file"},
		{Name: "one-byte.bin", Data: []byte("x"), Parent: testDir, Role: "one_byte_file"},
		{Name: "space name.txt", Data: []byte("space-name"), Parent: testDir, Role: "space_name_file"},
		{Name: "unicode-中文.txt", Data: []byte("unicode-name"), Parent: testDir, Role: "unicode_file"},
	}
	if nestedDir.ID != "" {
		fileCases = append(fileCases, crudFileCase{Name: "nested.txt", Data: []byte("nested-file"), Parent: nestedDir, Role: "nested_file"})
	}

	var files []drive.Entry
	for _, tc := range fileCases {
		entry, ok := runCRUDPutReadCase(ctx, d, result, tc)
		if ok {
			files = append(files, entry)
		}
	}

	var renamed drive.Entry
	if len(files) > 0 {
		renamed = files[0]
		oldName := renamed.Name
		newName := "renamed.txt"
		s = result.newStep("rename", newName)
		s.Input = map[string]any{"id": renamed.ID, "old_name": oldName, "new_name": newName}
		s.Expected = map[string]any{"old_listed": false, "new_listed": true}
		start = time.Now()
		err = d.Rename(stepContext(ctx, s), renamed, newName)
		if err == nil {
			renamed.Name = newName
		}
		s.Actual = map[string]any{"entry": entryActual(renamed)}
		s.finish(start, err)
		result.addStep(s)

		if err == nil {
			s = result.newStep("verify_rename_list", newName)
			s.Input = map[string]any{"parent_id": renamed.ParentID, "old_name": oldName, "new_name": newName}
			s.Expected = map[string]any{"old_listed": false, "new_listed": true}
			start = time.Now()
			stepCtx := stepContext(ctx, s)
			oldErr := verifyListNotContains(stepCtx, d, renamed.ParentID, oldName)
			newEntry, newErr := verifyListContains(stepCtx, d, renamed.ParentID, newName)
			err = firstErr(oldErr, newErr)
			s.Actual = map[string]any{
				"old_listed": oldErr != nil,
				"new_listed": newErr == nil,
				"new_entry":  entryActual(newEntry),
			}
			s.finish(start, err)
			result.addStep(s)
		}

		cleanupCRUDEntry(ctx, d, result, "renamed_file", renamed)
		files = files[1:]
	}

	for _, entry := range files {
		cleanupCRUDEntry(ctx, d, result, "file", entry)
	}
	if nestedDir.ID != "" {
		cleanupCRUDEntry(ctx, d, result, "nested_dir", nestedDir)
	}
	cleanupCRUDEntry(ctx, d, result, "test_dir", testDir)

	parentForVerify := testDir.ParentID
	if parentForVerify == "" {
		parentForVerify = rootID
	}
	s = result.newStep("verify_cleanup_list", parentForVerify)
	s.Input = map[string]any{"parent_id": parentForVerify, "test_prefix": testName}
	s.Expected = map[string]any{"residual_count": 0}
	start = time.Now()
	residual, timeline, err := residualEntries(stepContext(ctx, s), d, parentForVerify, testName)
	result.ResidualTimeline = timeline
	for _, entry := range residual {
		result.Residual = append(result.Residual, artifactFromEntry("residual", entry, "matches test prefix after cleanup"))
	}
	s.Actual = map[string]any{"residual_count": len(residual), "residual": residualNames(residual)}
	s.finish(start, err)
	result.addStep(s)
	return result
}

type crudFileCase struct {
	Name   string
	Data   []byte
	Parent drive.Entry
	Role   string
}

func runCRUDPutReadCase(ctx context.Context, d drive.Driver, result *CRUDTestResult, tc crudFileCase) (drive.Entry, bool) {
	s := result.newStep("put", tc.Name)
	s.Input = map[string]any{"parent_id": tc.Parent.ID, "name": tc.Name, "bytes": len(tc.Data)}
	s.Expected = map[string]any{"name": tc.Name, "size": len(tc.Data), "is_dir": false}
	start := time.Now()
	entry, err := d.PutSource(stepContext(ctx, s), drive.UploadRequest{
		ParentID: tc.Parent.ID,
		Name:     tc.Name,
		Source:   drive.NewBytesReadOnlyFileSource(tc.Data),
	})
	s.Actual = entryActual(entry)
	s.finish(start, err)
	result.addStep(s)
	if err != nil {
		return drive.Entry{}, false
	}
	result.addCreated(tc.Role, entry)

	s = result.newStep("verify_put_list", tc.Name)
	s.Input = map[string]any{"parent_id": tc.Parent.ID, "name": tc.Name}
	s.Expected = map[string]any{"listed": true}
	start = time.Now()
	listed, err := verifyListContains(stepContext(ctx, s), d, tc.Parent.ID, tc.Name)
	s.Actual = map[string]any{"listed": err == nil, "entry": entryActual(listed)}
	s.finish(start, err)
	result.addStep(s)

	s = result.newStep("read", tc.Name)
	s.Input = map[string]any{"id": entry.ID, "offset": 0, "size": len(tc.Data)}
	s.Expected = map[string]any{"bytes": len(tc.Data), "content_match": true}
	start = time.Now()
	data, readErr := readDriverEntry(stepContext(ctx, s), d, entry, int64(len(tc.Data)))
	if readErr == nil && !bytes.Equal(data, tc.Data) {
		readErr = fmt.Errorf("content mismatch: got %q, want %q", string(data), string(tc.Data))
	}
	s.Actual = map[string]any{"bytes": len(data), "content_match": readErr == nil}
	s.finish(start, readErr)
	result.addStep(s)
	return entry, true
}

func readDriverEntry(ctx context.Context, d drive.Driver, entry drive.Entry, size int64) ([]byte, error) {
	rc, err := d.Read(ctx, entry, 0, size)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func cleanupCRUDEntry(ctx context.Context, d drive.Driver, result *CRUDTestResult, role string, entry drive.Entry) {
	step := result.newStep("cleanup_remove", entry.Name)
	start := time.Now()
	err := d.Remove(stepContext(ctx, step), entry)
	duration := time.Since(start)
	item := CRUDCleanupResult{
		Operation:  "remove",
		Role:       role,
		Name:       entry.Name,
		ID:         entry.ID,
		OK:         err == nil,
		Duration:   duration.String(),
		DurationMS: durationMillis(duration),
	}
	if err != nil {
		item.Error = err.Error()
		item.ErrorCategory = drive.ErrorCategory(err)
		result.Residual = append(result.Residual, artifactFromEntry(role, entry, "cleanup failed: "+err.Error()))
	}
	result.Cleanup = append(result.Cleanup, item)
}

func stepContext(ctx context.Context, step CRUDTestStep) context.Context {
	return drive.WithDebugOperation(ctx, drive.DebugOperation{
		OpID: step.OpID,
		Step: step.Operation,
		Name: step.Name,
	})
}

func entryActual(entry drive.Entry) map[string]any {
	out := map[string]any{}
	if entry.ID != "" {
		out["id"] = entry.ID
	}
	if entry.ParentID != "" {
		out["parent_id"] = entry.ParentID
	}
	if entry.Name != "" {
		out["name"] = entry.Name
	}
	out["is_dir"] = entry.IsDir
	out["size"] = entry.Size
	return out
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func verifyListContains(ctx context.Context, d drive.Driver, parentID string, name string) (drive.Entry, error) {
	const maxAttempts = 3
	delay := 1 * time.Second
	var lastEntries []drive.Entry
	for attempt := range maxAttempts {
		if attempt > 0 {
			time.Sleep(delay)
			delay *= 2
		}
		entries, err := d.List(ctx, parentID)
		if err != nil {
			return drive.Entry{}, fmt.Errorf("list %q: %w", parentID, err)
		}
		lastEntries = entries
		for _, entry := range entries {
			if entry.Name == name {
				return entry, nil
			}
		}
	}
	return drive.Entry{}, fmt.Errorf("entry %q not listed under %q after %d attempts: %v", name, parentID, maxAttempts, entryNames(lastEntries))
}

func verifyListNotContains(ctx context.Context, d drive.Driver, parentID string, name string) error {
	const maxAttempts = 3
	delay := 1 * time.Second
	var lastEntries []drive.Entry
	for attempt := range maxAttempts {
		if attempt > 0 {
			time.Sleep(delay)
			delay *= 2
		}
		entries, err := d.List(ctx, parentID)
		if err != nil {
			return fmt.Errorf("list %q: %w", parentID, err)
		}
		lastEntries = entries
		found := false
		for _, entry := range entries {
			if entry.Name == name {
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return fmt.Errorf("entry %q still listed under %q after %d attempts: %v", name, parentID, maxAttempts, entryNames(lastEntries))
}

func residualEntries(ctx context.Context, d drive.Driver, parentID string, testPrefix string) ([]drive.Entry, []CRUDVisibilitySample, error) {
	const maxAttempts = 7
	delay := 1 * time.Second
	var residual []drive.Entry
	var timeline []CRUDVisibilitySample
	started := time.Now()
	for attempt := range maxAttempts {
		if attempt > 0 {
			time.Sleep(delay)
			delay *= 2
		}
		entries, err := d.List(ctx, parentID)
		if err != nil {
			elapsed := time.Since(started)
			timeline = append(timeline, CRUDVisibilitySample{
				Attempt:   attempt + 1,
				Elapsed:   elapsed.String(),
				ElapsedMS: durationMillis(elapsed),
				Error:     err.Error(),
			})
			return nil, timeline, fmt.Errorf("verify cleanup list: %w", err)
		}
		residual = residual[:0]
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name, testPrefix) {
				residual = append(residual, entry)
			}
		}
		elapsed := time.Since(started)
		timeline = append(timeline, CRUDVisibilitySample{
			Attempt:       attempt + 1,
			Elapsed:       elapsed.String(),
			ElapsedMS:     durationMillis(elapsed),
			ResidualCount: len(residual),
			ResidualNames: residualNames(residual),
		})
		if len(residual) == 0 {
			return nil, timeline, nil
		}
	}
	return residual, timeline, fmt.Errorf("stale entries after cleanup: %v", entryNames(residual))
}

func entryNames(entries []drive.Entry) []string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return names
}

func residualNames(entries []drive.Entry) []string {
	return entryNames(entries)
}
