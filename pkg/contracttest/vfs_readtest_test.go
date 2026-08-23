package contracttest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	vfsread "github.com/yinzhenyu/qrypt/pkg/vfs/read"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

type mountedReadCleanupFS struct {
	*xferFakeFS
	removeErrs     []error
	removeDirErrs  []error
	removeCalls    int
	removeDirCalls int
	clearErr       error
}

type mountedSeekActiveFS struct {
	*xferFakeFS
	active []diagnostics.DebugActiveMount
}

func (f *mountedSeekActiveFS) DebugActiveOps(context.Context, []string) ([]diagnostics.DebugActiveMount, error) {
	return f.active, nil
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

	seekResult := RunVFSMountedReadTest(ctx, fs, "local", DriverTestRequest{
		Mount: "local", MountPoint: remote, Size: "64k", CacheMode: "cold", Samples: 1,
		ReadPattern: "seek", SeekCount: 1, SeekSize: "4k",
	})
	if seekResult.Pass || len(seekResult.SeekMeasurements) != 1 {
		t.Fatalf("backend directory seek result = %+v", seekResult)
	}
	seekMeasurement := seekResult.SeekMeasurements[0]
	if seekMeasurement.Offset != 60<<10 || seekMeasurement.Bytes != 4<<10 || seekMeasurement.TraversedVFS {
		t.Fatalf("invalid rejected seek measurement: %+v", seekMeasurement)
	}
	if got := seekStepError(seekResult.Steps, "cold_seek"); got != "mounted seek read did not traverse qrypt; verify --mount-point" {
		t.Fatalf("cold seek error = %q", got)
	}
}

func seekStepError(steps []FSTestStep, operation string) string {
	for _, step := range steps {
		if step.Operation == operation {
			return step.Error
		}
	}
	return ""
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

func TestMountedSeekOffsetsAreDistinctAndSpanFile(t *testing.T) {
	if got := mountedSeekOffsets(100, 10, 3); !slices.Equal(got, []int64{30, 60, 90}) {
		t.Fatalf("offsets = %v, want [30 60 90]", got)
	}
	if got := mountedSeekOffsets(10, 10, 8); !slices.Equal(got, []int64{0}) {
		t.Fatalf("single-probe offsets = %v, want [0]", got)
	}
}

func TestMountedSeekChunksTouched(t *testing.T) {
	if got := mountedSeekChunksTouched(2<<20, 1<<20); got != 1 {
		t.Fatalf("aligned chunks = %d, want 1", got)
	}
	if got := mountedSeekChunksTouched((2<<20)+(512<<10), 1<<20); got != 2 {
		t.Fatalf("unaligned chunks = %d, want 2", got)
	}
}

func TestMountedVFSReadAtOffsetRequiresMatchingDataRead(t *testing.T) {
	events := []drive.MetricEvent{
		{Kind: "vfs_read", Phase: "fetch_window", Offset: 32, Bytes: 4096},
		{Kind: "vfs_read", Phase: "read", Offset: 64, Bytes: 4096},
	}
	if mountedVFSReadAtOffset(events, 32) {
		t.Fatal("phase detail was accepted as the requested FUSE read")
	}
	if !mountedVFSReadAtOffset(events, 64) {
		t.Fatal("matching VFS read offset was not detected")
	}
	aligned := []drive.MetricEvent{{Kind: "vfs_read", Phase: "read", Offset: 48, Bytes: 32}}
	if !mountedVFSReadAtOffset(aligned, 64) {
		t.Fatal("read covering an aligned-down seek offset was not detected")
	}
}

func TestMeasureMountedSeekReportsLoadLatencyAndValidatesContent(t *testing.T) {
	data := make([]byte, 64<<10)
	fillMountedReadPattern(data, 0)
	file := t.TempDir() + "/data.bin"
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}

	measurement, _, err := measureMountedSeek(context.Background(), newXferFakeFS(), "local", file, 16<<10, 4<<10, "cold", 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if measurement.Offset != 16<<10 || measurement.Bytes != 4<<10 || measurement.Sample != 2 || measurement.Index != 3 {
		t.Fatalf("measurement identity = %+v", measurement)
	}
	if measurement.LoadBPS <= 0 || measurement.TotalMicros < measurement.SeekMicros {
		t.Fatalf("measurement timing = %+v", measurement)
	}

	data[16<<10] ^= 0xff
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := measureMountedSeek(context.Background(), newXferFakeFS(), "local", file, 16<<10, 4<<10, "cold", 1, 1); err == nil {
		t.Fatal("corrupt seek range was accepted")
	}
}

func TestMountedSeekWarmupOffsetsAvoidTargetAndEachOther(t *testing.T) {
	const (
		fileSize     = int64(64 << 20)
		targetOffset = int64(32 << 20)
		targetSize   = int64(1 << 20)
		warmupChunks = 2
	)
	cold, warm := mountedSeekWarmupOffsets(fileSize, targetOffset, targetSize, warmupChunks)
	loadSpan := int64(warmupChunks+vfsread.PrefetchLimit*vfsread.SequentialPrefetchChunks) * vfsread.ChunkSize
	if cold == warm {
		t.Fatalf("warmup offsets = (%d, %d), want distinct ranges", cold, warm)
	}
	for _, offset := range []int64{cold, warm} {
		if rangesOverlap(offset, offset+loadSpan, targetOffset, targetOffset+targetSize) {
			t.Fatalf("warmup range [%d,%d) overlaps target", offset, offset+loadSpan)
		}
	}
	if rangesOverlap(cold, cold+loadSpan, warm, warm+loadSpan) {
		t.Fatalf("warmup ranges overlap: cold=%d warm=%d span=%d", cold, warm, loadSpan)
	}
}

func TestFindMountedSeekOverlapFiltersScenarioAndPath(t *testing.T) {
	const path = "/local/data.bin"
	mounts := []diagnostics.DebugActiveMount{{
		Mount: "local",
		Ops: []vfstypes.DebugActiveOp{
			{Kind: "vfs_prefetch", Phase: "fetch_window", Path: "/local/other.bin"},
			{Kind: "vfs_window_load", Phase: "fetch_window", Path: path, Offset: 1 << 20, Requested: 1 << 20},
			{Kind: "vfs_prefetch", Phase: "fetch_window", Path: path, Offset: 2 << 20, Requested: 2 << 20},
			{Kind: "vfs_prefetch", Phase: "acquire_slot", Path: path},
		},
	}}

	prefetch := findMountedSeekOverlap(mounts, path, "prefetch")
	if !prefetch.Found || prefetch.Count != 1 || prefetch.Kind != "vfs_prefetch" || prefetch.Offset != 2<<20 {
		t.Fatalf("prefetch overlap = %+v", prefetch)
	}
	concurrent := findMountedSeekOverlap(mounts, path, "concurrent")
	if !concurrent.Found || concurrent.Count != 2 || concurrent.Kind != "vfs_window_load" {
		t.Fatalf("concurrent overlap = %+v", concurrent)
	}
}

func TestMeasureMountedSeekScenariosRecordOverlap(t *testing.T) {
	data := make([]byte, 32<<20)
	fillMountedReadPattern(data, 0)
	file := t.TempDir() + "/data.bin"
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	const virtualPath = "/local/data.bin"
	fs := &mountedSeekActiveFS{
		xferFakeFS: newXferFakeFS(),
		active: []diagnostics.DebugActiveMount{{
			Mount: "local",
			Ops: []vfstypes.DebugActiveOp{{
				Kind: "vfs_prefetch", Phase: "fetch_window", Path: virtualPath,
				Offset: 2 << 20, Requested: 2 << 20,
			}},
		}},
	}

	for _, scenario := range []string{"prefetch", "concurrent"} {
		t.Run(scenario, func(t *testing.T) {
			measurement, _, err := measureMountedSeekScenario(
				context.Background(), fs, "local", file, virtualPath,
				16<<20, 1<<20, 0, scenario, 2, 100*time.Millisecond, "cold", 1, 1,
			)
			if err != nil {
				t.Fatal(err)
			}
			if measurement.Scenario != scenario || !measurement.Overlap || measurement.ActiveRequests != 1 {
				t.Fatalf("overlap measurement = %+v", measurement)
			}
			if measurement.ActiveKind != "vfs_prefetch" || measurement.ActivePhase != "fetch_window" || measurement.Bytes != 1<<20 {
				t.Fatalf("active read measurement = %+v", measurement)
			}
		})
	}
}

func TestWaitMountedSeekOverlapTimesOutWithoutActiveRead(t *testing.T) {
	fs := &mountedSeekActiveFS{xferFakeFS: newXferFakeFS()}
	overlap, err := waitMountedSeekOverlap(context.Background(), fs, "local", "/local/data.bin", "prefetch", 10*time.Millisecond)
	if err == nil || overlap.Found {
		t.Fatalf("overlap = %+v, err = %v; want timeout", overlap, err)
	}
}

func TestWaitMountedReadIdleRequiresQuietWindow(t *testing.T) {
	fs := &mountedSeekActiveFS{xferFakeFS: newXferFakeFS()}
	started := time.Now()
	if err := waitMountedReadIdle(context.Background(), fs, "local", "/local/data.bin", 100*time.Millisecond, true); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 10*time.Millisecond {
		t.Fatalf("idle wait returned after %s, before quiet window", elapsed)
	}
}

func TestWaitMountedReadIdleTimesOutForActiveRead(t *testing.T) {
	const path = "/local/data.bin"
	fs := &mountedSeekActiveFS{
		xferFakeFS: newXferFakeFS(),
		active: []diagnostics.DebugActiveMount{{
			Mount: "local",
			Ops:   []vfstypes.DebugActiveOp{{Kind: "vfs_prefetch", Phase: "acquire_slot", Path: path}},
		}},
	}
	if err := waitMountedReadIdle(context.Background(), fs, "local", path, 15*time.Millisecond, true); err == nil {
		t.Fatal("active prefetch was treated as idle")
	}
	fs.active[0].Ops[0].Path = "/local/other.bin"
	if !hasMountedReadActive(fs.active, "/local/other.bin") || hasMountedReadActive(fs.active, path) {
		t.Fatalf("active path filter failed: %+v", fs.active)
	}
}

func TestWaitMountedReadIdleAllowsOptionalDiagnostics(t *testing.T) {
	fs := newXferFakeFS()
	if err := waitMountedReadIdle(context.Background(), fs, "local", "/local/data.bin", time.Second, false); err != nil {
		t.Fatalf("optional idle diagnostics failed: %v", err)
	}
	if err := waitMountedReadIdle(context.Background(), fs, "local", "/local/data.bin", time.Second, true); err == nil {
		t.Fatal("required idle diagnostics accepted unsupported filesystem")
	}
}

func TestSummarizeMountedSeeks(t *testing.T) {
	summary := summarizeMountedSeeks([]MountedSeekMeasurement{
		{Mode: "cold", SeekMicros: 1, LoadMicros: 100, TotalMicros: 101, LoadBPS: 10},
		{Mode: "cold", SeekMicros: 3, LoadMicros: 300, TotalMicros: 303, LoadBPS: 30},
		{Mode: "warm", SeekMicros: 2, LoadMicros: 20, TotalMicros: 22, LoadBPS: 100},
	})
	if summary == nil || summary.Cold == nil || summary.Warm == nil {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.Cold.Samples != 2 || summary.Cold.P95LoadMicros != 300 || summary.Cold.MaxLoadBPS != 30 {
		t.Fatalf("cold summary = %+v", summary.Cold)
	}
	if summary.Warm.MedianLoadMicros != 20 || summary.Warm.MedianTotalMicros != 22 {
		t.Fatalf("warm summary = %+v", summary.Warm)
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
		Measurements:     []MountedReadMeasurement{{Mode: "cold", Sample: 1, Bytes: 4}},
		SeekMeasurements: []MountedSeekMeasurement{{Mode: "cold", Sample: 1, Offset: 4, Bytes: 4}},
		Steps:            []FSTestStep{{Operation: "cold_read", OK: true, Actual: map[string]any{"bytes": int64(4)}}},
		Metrics:          []drive.MetricEvent{{Kind: "vfs_read"}},
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
	if _, ok := read["seek_measurements"]; !ok {
		t.Fatalf("seek measurements missing: %s", body)
	}
}
