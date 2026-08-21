package contracttest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type mountedReadCleanupFS struct {
	*xferFakeFS
	removeErrs     []error
	removeDirErrs  []error
	removeCalls    int
	removeDirCalls int
	clearErr       error
}

func newMountedReadCleanupFS() *mountedReadCleanupFS {
	return &mountedReadCleanupFS{xferFakeFS: newXferFakeFS()}
}

func (f *mountedReadCleanupFS) Remove(context.Context, string) error {
	f.removeCalls++
	if len(f.removeErrs) == 0 {
		return nil
	}
	err := f.removeErrs[0]
	f.removeErrs = f.removeErrs[1:]
	return err
}

func (f *mountedReadCleanupFS) RemoveDir(context.Context, string) error {
	f.removeDirCalls++
	if len(f.removeDirErrs) == 0 {
		return nil
	}
	err := f.removeDirErrs[0]
	f.removeDirErrs = f.removeDirErrs[1:]
	return err
}

func (f *mountedReadCleanupFS) ClearReadCacheForMount(string) error {
	return f.clearErr
}

func TestRunVFSMountedReadTestRejectsBackendDirectoryAsMountPoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	driver := localfs.New(remote)
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(driver, vfs.Options{
		Name: "local", StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20,
		UploadDelay: time.Millisecond, DeleteDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	defer func() {
		cancel()
		_ = fs.Close(context.Background())
	}()

	result := RunVFSMountedReadTest(ctx, fs, "local", DriverTestRequest{
		Mount: "local", MountPoint: remote, Size: "64k", BlockSize: "4k",
		CacheMode: "both", Samples: 1,
	})
	if result.Pass {
		t.Fatalf("backend directory was accepted as a qrypt mount point: %+v", result)
	}
	if len(result.Measurements) != 1 {
		t.Fatalf("measurements = %d, want rejected cold measurement", len(result.Measurements))
	}
	measurement := result.Measurements[0]
	if measurement.Bytes != 64<<10 || measurement.TraversedVFS || measurement.VFSReadCalls != 0 {
		t.Fatalf("invalid rejected measurement: %+v", measurement)
	}
	var readErr string
	for _, step := range result.Steps {
		if step.Operation == "cold_read" {
			readErr = step.Error
			break
		}
	}
	if readErr != "mounted read did not traverse qrypt; verify --mount-point" {
		t.Fatalf("cold read error = %q", readErr)
	}
	if result.CleanupFailed || result.Steps[len(result.Steps)-1].Operation != "cleanup" {
		t.Fatalf("cleanup result = failed:%v last:%+v", result.CleanupFailed, result.Steps[len(result.Steps)-1])
	}
}

func TestMountedTestPathRequiresDirectory(t *testing.T) {
	if _, err := mountedTestPath("", "/data.bin"); err == nil {
		t.Fatal("empty mount point accepted")
	}
	file := t.TempDir() + "/file"
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mountedTestPath(file, "/data.bin"); err == nil {
		t.Fatal("file mount point accepted")
	}
}

func TestMeasureMountedFileIncludesFinalPartialPeakWindow(t *testing.T) {
	data := make([]byte, 64<<10)
	fillMountedReadPattern(data, 0)
	file := t.TempDir() + "/data.bin"
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(data)

	measurement, _, err := measureMountedFile(
		context.Background(), newXferFakeFS(), "local", file, fmt.Sprintf("%x", wantHash), 4<<10, "cold", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if measurement.PeakWindowBPS <= 0 {
		t.Fatalf("peak window throughput = %d, want positive value", measurement.PeakWindowBPS)
	}
}

func TestCleanupMountedReadFixtureRecoversFromTransientErrors(t *testing.T) {
	fs := newMountedReadCleanupFS()
	fs.removeErrs = []error{errors.New("temporary remove failure"), nil}
	fs.removeDirErrs = []error{errors.New("directory is not empty"), nil}

	if err := cleanupMountedReadFixture(context.Background(), fs, "/file", "/dir", "local"); err != nil {
		t.Fatalf("cleanup failed after successful retries: %v", err)
	}
	if fs.removeCalls != 2 || fs.removeDirCalls != 2 {
		t.Fatalf("remove calls = (%d, %d), want (2, 2)", fs.removeCalls, fs.removeDirCalls)
	}
}

func TestCleanupMountedReadFixtureReportsFinalDirectoryFailure(t *testing.T) {
	fs := newMountedReadCleanupFS()
	fs.removeDirErrs = []error{
		errors.New("directory is not empty"),
		errors.New("directory is not empty"),
		errors.New("directory is not empty"),
	}

	err := cleanupMountedReadFixture(context.Background(), fs, "/file", "/dir", "local")
	if err == nil {
		t.Fatal("cleanup accepted a directory that remained non-empty")
	}
	if fs.removeDirCalls != 3 {
		t.Fatalf("remove dir calls = %d, want 3", fs.removeDirCalls)
	}
}

func TestRunVFSMountedReadTestRecordsDeferredCleanupFailure(t *testing.T) {
	fs := newMountedReadCleanupFS()
	fs.clearErr = errors.New("clear cache failed")
	fs.removeErrs = []error{
		errors.New("remove failed"),
		errors.New("remove failed"),
		errors.New("remove failed"),
	}

	result := RunVFSMountedReadTest(context.Background(), fs, "local", DriverTestRequest{
		Mount: "local", MountPoint: t.TempDir(), Size: "1", BlockSize: "1", CacheMode: "cold", Samples: 1,
	})
	if result.Pass {
		t.Fatalf("read test passed despite read and cleanup failures: %+v", result)
	}
	if !result.CleanupFailed {
		t.Fatalf("CleanupFailed = false, want true: %+v", result.Steps)
	}
	last := result.Steps[len(result.Steps)-1]
	if last.Operation != "cleanup" || last.OK {
		t.Fatalf("last step = %+v, want failed cleanup", last)
	}
}

func TestMountedReadTestRunDoesNotDuplicateStepsOrMetrics(t *testing.T) {
	result := MountedReadTestResult{
		OpID: "read-1", Mount: "local", Pass: true,
		Measurements: []MountedReadMeasurement{{Mode: "cold", Sample: 1, Bytes: 4}},
		Steps:        []FSTestStep{{Operation: "cold_read", OK: true, Actual: map[string]any{"bytes": int64(4)}}},
		Metrics:      []drive.MetricEvent{{Kind: "vfs_read"}},
	}
	body, err := json.Marshal(fromMountedReadTestResult(result))
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	read, ok := envelope["read"].(map[string]any)
	if !ok {
		t.Fatalf("read details missing: %s", body)
	}
	for _, duplicate := range []string{"steps", "metrics"} {
		if _, ok := read[duplicate]; ok {
			t.Fatalf("read details duplicate top-level %q: %s", duplicate, body)
		}
	}
	if _, ok := read["measurements"]; !ok {
		t.Fatalf("read measurements missing: %s", body)
	}
}
