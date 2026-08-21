package contracttest

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const (
	DefaultBatchTestCount = 50
	MaxBatchTestCount     = 100
	DefaultBatchTestSize  = 4 << 10
	MaxBatchTestSize      = 1 << 20
)

type BatchTestResult struct {
	OpID            string              `json:"op_id"`
	Spec            string              `json:"spec"`
	Mount           string              `json:"mount"`
	Pass            bool                `json:"pass"`
	Steps           []FSTestStep        `json:"steps"`
	PendingTimeline []FSPendingSample   `json:"pending_timeline,omitempty"`
	Metrics         []drive.MetricEvent `json:"metrics,omitempty"`
	Started         time.Time           `json:"started_at"`
	Finished        time.Time           `json:"finished_at"`
	Duration        string              `json:"duration"`
	DurationMS      int64               `json:"duration_ms"`
	RetryCommand    string              `json:"retry_command,omitempty"`
}

type batchTest struct {
	ctx     context.Context
	fs      vfs.FileSystem
	result  *BatchTestResult
	mount   string
	count   int
	size    int64
	baseDir string
	files   []string
	cleaned bool
}

func RunVFSBatchUploadTest(ctx context.Context, fs vfs.FileSystem, mount string, count int, size int64) *BatchTestResult {
	t := newBatchTest(ctx, fs, "batchupload", mount, count, size)
	defer t.finish()
	if !t.validateOptions() {
		return t.result
	}
	t.baseDir = fsTestBasePath(fs, mount) + "/__qrypt_batchupload_test_" + randomSuffix(6)
	t.files = batchFilePaths(t.baseDir, t.count)

	if !t.mkdir("mkdir", t.baseDir) || !t.uploadFiles("batch_write", t.files, 0) || !t.waitIdle("wait_upload") {
		return t.result
	}
	if !t.verifyParentCache() || !t.verifyFiles("verify_upload", t.baseDir, t.files, 0) {
		return t.result
	}
	t.cleanup(t.files, t.baseDir)
	return t.result
}

func RunVFSBatchMoveTest(ctx context.Context, fs vfs.FileSystem, mount string, count int, size int64) *BatchTestResult {
	t := newBatchTest(ctx, fs, "batchmove", mount, count, size)
	defer t.finish()
	if !t.validateOptions() {
		return t.result
	}
	t.baseDir = fsTestBasePath(fs, mount) + "/__qrypt_batchmove_test_" + randomSuffix(6)
	sourceDir := t.baseDir + "/source"
	destDir := t.baseDir + "/dest"
	sourceFiles := batchFilePaths(sourceDir, t.count)
	destFiles := batchFilePaths(destDir, t.count)
	t.files = append(append([]string{}, sourceFiles...), destFiles...)

	if !t.mkdir("mkdir_root", t.baseDir) || !t.mkdir("mkdir_source", sourceDir) ||
		!t.mkdir("mkdir_dest", destDir) || !t.uploadFiles("seed_upload", sourceFiles, 0) ||
		!t.waitIdle("wait_seed_upload") {
		return t.result
	}

	step := fsStep("batch_move")
	step.Input = map[string]any{"source": sourceDir, "dest": destDir, "count": t.count}
	started := time.Now()
	var moveErr error
	moved := 0
	for i := range sourceFiles {
		if err := t.fs.Rename(t.ctx, sourceFiles[i], destFiles[i]); err != nil {
			moveErr = fmt.Errorf("rename file %d: %w", i, err)
			break
		}
		moved++
	}
	step.Actual = map[string]any{"moved": moved}
	step.finish(started, moveErr)
	t.result.Steps = append(t.result.Steps, step)
	t.addMetric("batch_move", started, int64(t.count)*t.size, map[string]any{"count": t.count})
	if moveErr != nil {
		return t.result
	}
	if !t.verifyFiles("verify_move", destDir, destFiles, 0) || !t.verifyEmpty("verify_source_empty", sourceDir) {
		return t.result
	}
	t.cleanup(destFiles, destDir, sourceDir, t.baseDir)
	return t.result
}

func newBatchTest(ctx context.Context, fs vfs.FileSystem, spec, mount string, count int, size int64) *batchTest {
	return &batchTest{
		ctx: ctx, fs: fs, mount: mount, count: count, size: size,
		result: &BatchTestResult{
			OpID: newDebugOperationID(spec), Spec: spec, Mount: mount, Started: time.Now(),
			Steps:        make([]FSTestStep, 0, 12),
			RetryCommand: fmt.Sprintf("qrypt debug test %s --mount %s --count %d --size %d --socket PATH", spec, mount, count, size),
		},
	}
}

func (t *batchTest) validateOptions() bool {
	if t.count == 0 {
		t.count = DefaultBatchTestCount
	}
	if t.size == 0 {
		t.size = DefaultBatchTestSize
	}
	t.result.RetryCommand = fmt.Sprintf("qrypt debug test %s --mount %s --count %d --size %d --socket PATH", t.result.Spec, t.mount, t.count, t.size)
	var err error
	if t.count < 1 || t.count > MaxBatchTestCount {
		err = fmt.Errorf("count must be between 1 and %d", MaxBatchTestCount)
	} else if t.size < 1 || t.size > MaxBatchTestSize {
		err = fmt.Errorf("size must be between 1 and %d bytes", MaxBatchTestSize)
	}
	step := fsStep("validate_options")
	step.Input = map[string]any{"count": t.count, "size": t.size}
	step.finish(time.Now(), err)
	t.result.Steps = append(t.result.Steps, step)
	return err == nil
}

func (t *batchTest) finish() {
	if t.baseDir != "" && !t.cleaned {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.ctx), 30*time.Second)
		defer cancel()
		for _, file := range t.files {
			_ = t.fs.Remove(cleanupCtx, file)
		}
		_ = t.fs.RemoveDir(cleanupCtx, t.baseDir+"/dest")
		_ = t.fs.RemoveDir(cleanupCtx, t.baseDir+"/source")
		_ = t.fs.RemoveDir(cleanupCtx, t.baseDir)
	}
	t.result.Finished = time.Now()
	duration := t.result.Finished.Sub(t.result.Started)
	t.result.Duration = duration.String()
	t.result.DurationMS = DurationMillis(duration)
	t.result.Pass = len(t.result.Steps) > 0
	for _, step := range t.result.Steps {
		if !step.OK {
			t.result.Pass = false
			break
		}
	}
	t.result.Metrics = append(t.result.Metrics, drive.MetricEvent{
		At: t.result.Finished, OpID: t.result.OpID, Kind: "vfs_batch", Layer: "vfs",
		Mount: t.mount, Operation: t.result.Spec, Phase: "total", OK: t.result.Pass,
		Bytes: int64(t.count) * t.size, Duration: t.result.Duration, DurationMS: t.result.DurationMS,
		StartedAt: t.result.Started, FinishedAt: t.result.Finished,
		Extra: map[string]any{"count": t.count, "file_size": t.size},
	})
}

func (t *batchTest) mkdir(operation, dir string) bool {
	step := fsStep(operation)
	step.Input = map[string]any{"path": dir}
	started := time.Now()
	_, err := t.fs.Mkdir(t.ctx, dir)
	step.finish(started, err)
	t.result.Steps = append(t.result.Steps, step)
	return err == nil
}

func (t *batchTest) uploadFiles(operation string, files []string, generation int) bool {
	step := fsStep(operation)
	step.Input = map[string]any{"count": len(files), "file_size": t.size}
	started := time.Now()
	var err error
	completed := 0
	for i, file := range files {
		data := batchFileData(i, generation, t.size)
		if err = t.fs.Create(t.ctx, file); err == nil {
			_, err = t.fs.WriteAt(t.ctx, file, data, 0)
		}
		if err == nil {
			err = t.fs.Flush(t.ctx, file)
		}
		if err != nil {
			err = fmt.Errorf("write file %d: %w", i, err)
			break
		}
		completed++
	}
	step.Actual = map[string]any{"completed": completed}
	step.finish(started, err)
	t.result.Steps = append(t.result.Steps, step)
	t.addMetric(operation, started, int64(completed)*t.size, map[string]any{"count": completed})
	return err == nil
}

func (t *batchTest) waitIdle(operation string) bool {
	step := fsStep(operation)
	step.Expected = map[string]any{"pending_count": 0, "upload_count": 0}
	started := time.Now()
	err := waitVFSSmokeIdle(t.ctx, t.fs, t.mount, 5*time.Minute, &t.result.PendingTimeline)
	step.Actual = map[string]any{"state": fsMountState(t.fs, t.mount)}
	step.finish(started, err)
	t.result.Steps = append(t.result.Steps, step)
	t.addMetric(operation, started, int64(t.count)*t.size, nil)
	return err == nil
}

func (t *batchTest) verifyParentCache() bool {
	step := fsStep("verify_parent_cache")
	step.Expected = map[string]any{"misses_max": 1, "reuse_min": t.count - 1}
	started := time.Now()
	available, observed, misses, hits, shared := batchParentCacheStats(t.fs, t.files)
	step.Actual = map[string]any{"observed": observed, "misses": misses, "hits": hits, "shared": shared}
	var err error
	if available && (observed != t.count || misses > 1 || hits+shared < t.count-1) {
		err = fmt.Errorf("parent cache was not reused: observed=%d misses=%d hits=%d shared=%d", observed, misses, hits, shared)
	}
	step.finish(started, err)
	t.result.Steps = append(t.result.Steps, step)
	t.result.Metrics = append(t.result.Metrics, drive.MetricEvent{
		At: time.Now(), OpID: t.result.OpID, Kind: "vfs_batch", Layer: "vfs", Mount: t.mount,
		Operation: t.result.Spec, Phase: "parent_cache", OK: err == nil,
		CacheHits: int64(hits + shared), CacheMisses: int64(misses),
		Extra: map[string]any{"observed": observed, "hits": hits, "shared": shared},
	})
	return err == nil
}

func (t *batchTest) verifyFiles(operation, dir string, files []string, generation int) bool {
	step := fsStep(operation)
	step.Expected = map[string]any{"count": len(files), "sample_content_match": true}
	started := time.Now()
	if refresher, ok := t.fs.(vfs.PathRefresher); ok {
		refresher.RefreshPath(dir)
	}
	entries, err := t.fs.List(t.ctx, dir)
	if err == nil {
		actual := make(map[string]drive.Entry, len(entries))
		for _, entry := range entries {
			actual[entry.Name] = entry
		}
		if len(actual) != len(files) {
			err = fmt.Errorf("list count mismatch: got %d, want %d", len(actual), len(files))
		}
		for _, file := range files {
			entry, ok := actual[path.Base(file)]
			if err == nil && (!ok || entry.IsDir || entry.Size != t.size) {
				err = fmt.Errorf("invalid entry %s: found=%t dir=%t size=%d", file, ok, entry.IsDir, entry.Size)
			}
		}
	}
	if err == nil {
		err = verifyBatchSamples(t.ctx, t.fs, files, generation, t.size)
	}
	step.Actual = map[string]any{"count": len(entries), "sample_content_match": err == nil}
	step.finish(started, err)
	t.result.Steps = append(t.result.Steps, step)
	return err == nil
}

func (t *batchTest) verifyEmpty(operation, dir string) bool {
	step := fsStep(operation)
	started := time.Now()
	if refresher, ok := t.fs.(vfs.PathRefresher); ok {
		refresher.RefreshPath(dir)
	}
	entries, err := t.fs.List(t.ctx, dir)
	if err == nil && len(entries) != 0 {
		err = fmt.Errorf("source still contains %d entries", len(entries))
	}
	step.Actual = map[string]any{"count": len(entries)}
	step.finish(started, err)
	t.result.Steps = append(t.result.Steps, step)
	return err == nil
}

func (t *batchTest) cleanup(files []string, dirs ...string) {
	step := fsStep("cleanup")
	started := time.Now()
	var err error
	for _, file := range files {
		if removeErr := t.fs.Remove(t.ctx, file); removeErr != nil && err == nil {
			err = removeErr
		}
	}
	for _, dir := range dirs {
		if removeErr := t.fs.RemoveDir(t.ctx, dir); removeErr != nil && err == nil {
			err = removeErr
		}
	}
	if err == nil {
		err = waitVFSSmokeIdle(t.ctx, t.fs, t.mount, 2*time.Minute, &t.result.PendingTimeline)
	}
	step.finish(started, err)
	t.result.Steps = append(t.result.Steps, step)
	t.cleaned = err == nil
}

func (t *batchTest) addMetric(phase string, started time.Time, bytes int64, extra map[string]any) {
	duration := time.Since(started)
	event := drive.MetricEvent{
		At: time.Now(), OpID: t.result.OpID, Kind: "vfs_batch", Layer: "vfs", Mount: t.mount,
		Operation: t.result.Spec, Phase: phase, OK: true, Bytes: bytes,
		Duration: duration.String(), DurationMS: DurationMillis(duration), StartedAt: started, FinishedAt: time.Now(), Extra: extra,
	}
	if duration > 0 && bytes > 0 {
		event.Throughput = int64(float64(bytes) / duration.Seconds())
	}
	t.result.Metrics = append(t.result.Metrics, event)
}

func batchFilePaths(dir string, count int) []string {
	files := make([]string, count)
	for i := range files {
		files[i] = fmt.Sprintf("%s/file-%04d.bin", dir, i)
	}
	return files
}

func batchFileData(index, generation int, size int64) []byte {
	prefix := []byte(fmt.Sprintf("qrypt-batch-%04d-generation-%d\n", index, generation))
	data := make([]byte, size)
	for off := 0; off < len(data); off += len(prefix) {
		copy(data[off:], prefix)
	}
	return data
}

func verifyBatchSamples(ctx context.Context, fs vfs.FileSystem, files []string, generation int, size int64) error {
	if len(files) == 0 {
		return nil
	}
	indices := []int{0, len(files) / 2, len(files) - 1}
	sort.Ints(indices)
	last := -1
	for _, index := range indices {
		if index == last {
			continue
		}
		last = index
		got, err := readVFSFile(ctx, fs, files[index])
		if err != nil {
			return fmt.Errorf("read sample %d: %w", index, err)
		}
		if want := batchFileData(index, generation, size); !bytes.Equal(got, want) {
			return fmt.Errorf("sample %d content mismatch: got %d bytes, want %d", index, len(got), len(want))
		}
	}
	return nil
}

func batchParentCacheStats(fs vfs.FileSystem, files []string) (available bool, observed, misses, hits, shared int) {
	snapshotter, ok := fs.(vfsDebugSnapshotter)
	if !ok {
		return false, 0, 0, 0, 0
	}
	available = true
	snapshot := snapshotter.DebugSnapshot()
	for _, file := range files {
		upload, _, _, found := findVFSUpload(snapshot, file)
		if !found {
			continue
		}
		for _, event := range upload.Events {
			if event.Phase != "prepare_remote" {
				continue
			}
			status, _ := event.Extra["parent_cache"].(string)
			if status == "" {
				continue
			}
			observed++
			switch strings.ToLower(status) {
			case "miss":
				misses++
			case "hit":
				hits++
			case "shared":
				shared++
			}
			break
		}
	}
	return available, observed, misses, hits, shared
}
