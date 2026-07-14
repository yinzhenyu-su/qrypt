package control

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"sort"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type DriverCopyResult struct {
	OpID        string               `json:"op_id"`
	SourcePath  string               `json:"source_path"`
	DestPath    string               `json:"dest_path"`
	SourceMount string               `json:"source_mount"`
	DestMount   string               `json:"dest_mount"`
	SourceType  string               `json:"source_type,omitempty"`
	DestType    string               `json:"dest_type,omitempty"`
	Pass        bool                 `json:"pass"`
	Bytes       int64                `json:"bytes"`
	Started     time.Time            `json:"started_at"`
	Finished    time.Time            `json:"finished_at"`
	Duration    string               `json:"duration"`
	DurationMS  int64                `json:"duration_ms"`
	Steps       []TransferStep       `json:"steps"`
	Timeline    []TransferTraceEvent `json:"timeline,omitempty"`
	DestEntry   *drive.Entry         `json:"dest_entry,omitempty"`
}

type DriverCopyDirResult struct {
	OpID          string                  `json:"op_id"`
	SourcePath    string                  `json:"source_path"`
	DestPath      string                  `json:"dest_path"`
	Pass          bool                    `json:"pass"`
	Copied        int                     `json:"copied"`
	Skipped       int                     `json:"skipped"`
	Failed        int                     `json:"failed"`
	Bytes         int64                   `json:"bytes"`
	Started       time.Time               `json:"started_at"`
	Finished      time.Time               `json:"finished_at"`
	Duration      string                  `json:"duration"`
	DurationMS    int64                   `json:"duration_ms"`
	Error         string                  `json:"error,omitempty"`
	ErrorCategory string                  `json:"error_category,omitempty"`
	Entries       []DriverCopyEntryResult `json:"entries,omitempty"`
}

type DriverCopyEntryResult struct {
	OpID          string    `json:"op_id,omitempty"`
	Kind          string    `json:"kind"`
	State         string    `json:"state"`
	SourcePath    string    `json:"source_path"`
	DestPath      string    `json:"dest_path"`
	Bytes         int64     `json:"bytes,omitempty"`
	Error         string    `json:"error,omitempty"`
	ErrorCategory string    `json:"error_category,omitempty"`
	Started       time.Time `json:"started_at,omitempty"`
	Finished      time.Time `json:"finished_at,omitempty"`
	Duration      string    `json:"duration,omitempty"`
	DurationMS    int64     `json:"duration_ms,omitempty"`
}

type DriverCopySource interface {
	vfs.DriverProvider
	vfs.DebugResolver
	DebugSnapshot() vfs.DebugSnapshot
}

type copyResolvedPath struct {
	Path      string
	Mount     string
	Driver    string
	Info      vfs.DebugResolveInfo
	Drive     drive.Driver
	Entry     drive.Entry
	IsMissing bool
}

type directCopyProgress struct {
	current        drive.UploadPhase
	currentStarted time.Time
	stageDurations map[string]time.Duration
	bytesUploaded  int64
}

type CopyFileSystem interface {
	Stat(ctx context.Context, path string) (drive.Entry, error)
	List(ctx context.Context, path string) ([]drive.Entry, error)
	Mkdir(ctx context.Context, path string) (drive.Entry, error)
}

func RunDirectDriverCopy(ctx context.Context, source DriverCopySource, srcPath, dstPath string, overwrite bool) *DriverCopyResult {
	result := &DriverCopyResult{
		OpID:       newDebugOperationID("copy"),
		SourcePath: cleanVirtual(srcPath),
		DestPath:   cleanVirtual(dstPath),
		Started:    timeutil.Now(),
		Steps:      make([]TransferStep, 0, 8),
	}
	defer func() {
		result.Finished = timeutil.Now()
		duration := result.Finished.Sub(result.Started)
		result.Duration = duration.String()
		result.DurationMS = durationMillis(duration)
		if !result.Pass {
			return
		}
		for _, step := range result.Steps {
			if !step.OK {
				result.Pass = false
				return
			}
		}
	}()

	drivers := source.Drivers()

	start := timeutil.Now()
	src, err := resolveCopyPath(ctx, source, drivers, result.SourcePath)
	appendCopyStep(result, "resolve_source", 0, start, err)
	if err != nil {
		return result
	}
	result.SourceMount = src.Mount
	result.SourceType = src.Driver
	if src.Info.IsDir {
		appendCopyStep(result, "validate_source", 0, timeutil.Now(), fmt.Errorf("source is a directory"))
		return result
	}
	if src.Info.Pending {
		appendCopyStep(result, "validate_source", 0, timeutil.Now(), fmt.Errorf("source has pending local changes; flush/upload it before direct copy"))
		return result
	}
	if src.Info.RemoteID == "" {
		appendCopyStep(result, "validate_source", 0, timeutil.Now(), fmt.Errorf("source not found: %s", result.SourcePath))
		return result
	}

	dstParentPath, dstName := splitDestPath(result.DestPath)
	if dstName == "" || dstName == "/" {
		appendCopyStep(result, "validate_dest", 0, timeutil.Now(), fmt.Errorf("dest must include a file name"))
		return result
	}
	start = timeutil.Now()
	dstParent, err := resolveCopyPath(ctx, source, drivers, dstParentPath)
	appendCopyStep(result, "resolve_dest_parent", 0, start, err)
	if err != nil {
		return result
	}
	result.DestMount = dstParent.Mount
	result.DestType = dstParent.Driver
	if !dstParent.Info.IsDir {
		appendCopyStep(result, "validate_dest_parent", 0, timeutil.Now(), fmt.Errorf("dest parent is not a directory: %s", dstParentPath))
		return result
	}
	if result.SourceMount == "" || result.DestMount == "" {
		appendCopyStep(result, "validate_mounts", 0, timeutil.Now(), fmt.Errorf("source and dest mounts are required"))
		return result
	}

	if !drive.HasCapability(dstParent.Drive, drive.CapabilitySourceUploader) {
		appendCopyStep(result, "capability_check", 0, timeutil.Now(), fmt.Errorf("dest driver does not implement SourceUploader"))
		return result
	}
	dstHasWriter := drive.HasCapability(dstParent.Drive, drive.CapabilityWriter)

	start = timeutil.Now()
	existing, err := resolveCopyPath(ctx, source, drivers, result.DestPath)
	if err == nil && !existing.IsMissing && existing.Info.RemoteID != "" {
		if !overwrite {
			appendCopyStep(result, "check_dest_exists", 0, start, fmt.Errorf("dest already exists: %s", result.DestPath))
			return result
		}
		appendCopyStep(result, "check_dest_exists", 0, start, nil)
		if !dstHasWriter {
			appendCopyStep(result, "remove_existing", 0, timeutil.Now(), fmt.Errorf("dest exists and driver does not implement Writer"))
			return result
		}
		start := timeutil.Now()
		err = dstParent.Drive.Remove(ctx, existing.Entry)
		appendCopyStep(result, "remove_existing", 0, start, err)
		appendCopyTrace(result, "remove_existing", dstParent.Mount, dstParent.Driver, result.DestPath, 0, start, nil)
		if err != nil {
			return result
		}
	} else if err != nil && !vfs.IsNotFound(err) {
		appendCopyStep(result, "check_dest_exists", 0, start, err)
		return result
	} else {
		appendCopyStep(result, "check_dest_exists", 0, start, nil)
	}

	tmp, cleanup, hashes, err := copySourceToTemp(ctx, src.Drive, src.Entry, src.Info.Size)
	appendCopyStep(result, "read_source_to_temp", tmp.bytes, tmp.started, err)
	appendCopyTrace(result, "read_source_to_temp", src.Mount, src.Driver, result.SourcePath, tmp.bytes, tmp.started, nil)
	defer cleanup()
	if err != nil {
		return result
	}
	result.Bytes = tmp.bytes

	progress := &directCopyProgress{}
	start = timeutil.Now()
	destEntry, err := dstParent.Drive.PutSource(ctx, drive.UploadRequest{
		ParentID: dstParent.Info.RemoteID,
		Name:     dstName,
		Source:   drive.NewLocalReadOnlyFileSourceWithHashes(tmp.path, tmp.bytes, hashes),
		Progress: progress,
	})
	progress.finish()
	extra := map[string]any{
		"bytes_uploaded":  progress.bytesUploaded,
		"stage_durations": progress.stageDurationStrings(),
	}
	if destEntry.ID != "" {
		extra["entry_id"] = destEntry.ID
	}
	if err != nil {
		extra["error"] = err.Error()
	}
	appendCopyStep(result, "driver_put_source", tmp.bytes, start, err)
	appendCopyTrace(result, "driver_put_source", dstParent.Mount, dstParent.Driver, result.DestPath, tmp.bytes, start, extra)
	if err != nil {
		return result
	}

	result.DestEntry = &destEntry
	result.Pass = true
	return result
}

func RunDirectDriverCopyDir(ctx context.Context, fs CopyFileSystem, source DriverCopySource, srcPath, dstParentPath string, overwrite bool) *DriverCopyDirResult {
	started := timeutil.Now()
	result := &DriverCopyDirResult{
		OpID:       newDebugOperationID("copydir"),
		SourcePath: cleanVirtual(srcPath),
		DestPath:   pathpkg.Join(cleanVirtual(dstParentPath), pathpkg.Base(cleanVirtual(srcPath))),
		Started:    started,
		Entries:    []DriverCopyEntryResult{},
	}
	defer func() {
		result.Finished = timeutil.Now()
		duration := result.Finished.Sub(result.Started)
		result.Duration = duration.String()
		result.DurationMS = durationMillis(duration)
		result.Pass = result.Error == "" && result.Failed == 0
	}()
	if err := copyDirRecursive(ctx, fs, source, result.SourcePath, result.DestPath, overwrite, result); err != nil {
		result.Error = err.Error()
		result.ErrorCategory = drive.ErrorCategory(err)
	}
	return result
}

func copyDirRecursive(ctx context.Context, fs CopyFileSystem, source DriverCopySource, srcPath, dstPath string, overwrite bool, result *DriverCopyDirResult) error {
	if err := mkdirAllRemote(ctx, fs, dstPath); err != nil {
		result.recordEntry(DriverCopyEntryResult{Kind: "directory", State: "failed", SourcePath: srcPath, DestPath: dstPath, Error: err.Error(), ErrorCategory: drive.ErrorCategory(err)})
		return err
	}
	result.recordEntry(DriverCopyEntryResult{Kind: "directory", State: "ready", SourcePath: srcPath, DestPath: dstPath})
	entries, err := fs.List(ctx, srcPath)
	if err != nil {
		result.recordEntry(DriverCopyEntryResult{Kind: "directory", State: "failed", SourcePath: srcPath, DestPath: dstPath, Error: err.Error(), ErrorCategory: drive.ErrorCategory(err)})
		return err
	}
	for _, entry := range entries {
		childSrc := pathpkg.Join(srcPath, entry.Name)
		childDst := pathpkg.Join(dstPath, entry.Name)
		if entry.IsDir {
			if err := copyDirRecursive(ctx, fs, source, childSrc, childDst, overwrite, result); err != nil {
				return err
			}
			continue
		}
		if !overwrite {
			if _, err := fs.Stat(ctx, childDst); err == nil {
				result.Skipped++
				result.recordEntry(DriverCopyEntryResult{Kind: "file", State: "skipped", SourcePath: childSrc, DestPath: childDst})
				continue
			} else if !vfs.IsNotFound(err) {
				result.Failed++
				result.recordEntry(DriverCopyEntryResult{Kind: "file", State: "failed", SourcePath: childSrc, DestPath: childDst, Error: err.Error(), ErrorCategory: drive.ErrorCategory(err)})
				return err
			}
		}
		copyResult := RunDirectDriverCopy(ctx, source, childSrc, childDst, overwrite)
		entryResult := DriverCopyEntryResult{
			OpID:       copyResult.OpID,
			Kind:       "file",
			SourcePath: childSrc,
			DestPath:   childDst,
			Bytes:      copyResult.Bytes,
			Started:    copyResult.Started,
			Finished:   copyResult.Finished,
			Duration:   copyResult.Duration,
			DurationMS: copyResult.DurationMS,
		}
		if !copyResult.Pass {
			result.Failed++
			entryResult.State = "failed"
			entryResult.Error = firstCopyError(copyResult)
			entryResult.ErrorCategory = drive.ErrorCategoryMessage(entryResult.Error)
			result.recordEntry(entryResult)
			return fmt.Errorf("%s", entryResult.Error)
		}
		result.Copied++
		result.Bytes += copyResult.Bytes
		entryResult.State = "copied"
		result.recordEntry(entryResult)
	}
	return nil
}

func mkdirAllRemote(ctx context.Context, fs CopyFileSystem, dir string) error {
	dir = cleanVirtual(dir)
	if dir == "/" {
		return nil
	}
	current := "/"
	for _, part := range splitCleanPath(dir) {
		current = pathpkg.Join(current, part)
		entry, err := fs.Stat(ctx, current)
		if err == nil {
			if !entry.IsDir {
				return fmt.Errorf("remote destination %q exists and is not a directory", current)
			}
			continue
		}
		if !vfs.IsNotFound(err) {
			return err
		}
		if _, err := fs.Mkdir(ctx, current); err != nil {
			return err
		}
	}
	return nil
}

func splitCleanPath(path string) []string {
	path = strings.Trim(cleanVirtual(path), "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func (r *DriverCopyDirResult) recordEntry(entry DriverCopyEntryResult) {
	now := timeutil.Now()
	if entry.Started.IsZero() {
		entry.Started = now
	}
	if entry.Finished.IsZero() {
		entry.Finished = now
	}
	if entry.Duration == "" {
		duration := entry.Finished.Sub(entry.Started)
		entry.Duration = duration.String()
		entry.DurationMS = durationMillis(duration)
	}
	r.Entries = append(r.Entries, entry)
}

func firstCopyError(result *DriverCopyResult) string {
	for _, step := range result.Steps {
		if !step.OK && step.Error != "" {
			return step.Phase + ": " + step.Error
		}
	}
	return "copy failed"
}

func DriverCopyError(result *DriverCopyResult) string {
	return firstCopyError(result)
}

type tempCopy struct {
	path    string
	bytes   int64
	started time.Time
}

func copySourceToTemp(ctx context.Context, srcDriver drive.Driver, srcEntry drive.Entry, expectedSize int64) (tempCopy, func(), drive.SourceHashes, error) {
	start := timeutil.Now()
	cleanup := func() {}
	rc, err := srcDriver.Read(ctx, srcEntry, 0, 0)
	if err != nil {
		return tempCopy{started: start}, cleanup, nil, err
	}
	defer rc.Close()

	tmp, err := os.CreateTemp("", "qrypt-direct-copy-*")
	if err != nil {
		return tempCopy{started: start}, cleanup, nil, err
	}
	tmpPath := tmp.Name()
	cleanup = func() { _ = os.Remove(tmpPath) }

	md5Hash := md5.New()
	sha1Hash := sha1.New()
	sha256Hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, md5Hash, sha1Hash, sha256Hash), rc)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	switch {
	case copyErr != nil:
		return tempCopy{path: tmpPath, bytes: written, started: start}, cleanup, nil, copyErr
	case syncErr != nil:
		return tempCopy{path: tmpPath, bytes: written, started: start}, cleanup, nil, syncErr
	case closeErr != nil:
		return tempCopy{path: tmpPath, bytes: written, started: start}, cleanup, nil, closeErr
	case expectedSize > 0 && written != expectedSize:
		return tempCopy{path: tmpPath, bytes: written, started: start}, cleanup, nil, fmt.Errorf("source read size mismatch: got %d bytes, want %d", written, expectedSize)
	}
	return tempCopy{path: tmpPath, bytes: written, started: start}, cleanup, drive.SourceHashes{
		drive.HashMD5:    md5Hash.Sum(nil),
		drive.HashSHA1:   sha1Hash.Sum(nil),
		drive.HashSHA256: sha256Hash.Sum(nil),
	}, nil
}

func resolveCopyPath(ctx context.Context, source DriverCopySource, drivers []vfs.NamedDriver, virtualPath string) (copyResolvedPath, error) {
	virtualPath = cleanVirtual(virtualPath)
	info, err := source.DebugResolve(ctx, virtualPath, false)
	if err != nil {
		return copyResolvedPath{}, err
	}
	snapshot := source.DebugSnapshot()
	mountName := info.Mount
	if mountName == "" {
		mountName = cacheMountName(snapshot, virtualPath)
	}
	if mountName == "" && len(drivers) == 1 {
		mountName = drivers[0].Name
	}
	driver := findNamedDriver(drivers, mountName)
	if driver == nil {
		return copyResolvedPath{}, fmt.Errorf("mount %q not found", mountName)
	}
	missing := info.RemoteID == ""
	if missing {
		return copyResolvedPath{Path: virtualPath, Mount: mountName, Driver: info.Driver, Info: info, Drive: driver, IsMissing: true}, nil
	}
	if info.Driver == "" {
		if snap, err := driver.DebugSnapshot(ctx); err == nil {
			info.Driver = snap.Driver
		}
	}
	return copyResolvedPath{
		Path:   virtualPath,
		Mount:  mountName,
		Driver: info.Driver,
		Info:   info,
		Drive:  driver,
		Entry: drive.Entry{
			ID:       info.RemoteID,
			ParentID: info.ParentID,
			Name:     info.PlainName,
			IsDir:    info.IsDir,
			Size:     info.Size,
		},
	}, nil
}

func findNamedDriver(drivers []vfs.NamedDriver, name string) drive.Driver {
	for _, item := range drivers {
		if item.Name == name {
			return item.Driver
		}
	}
	return nil
}

func splitDestPath(path string) (string, string) {
	path = cleanVirtual(path)
	return cleanVirtual(pathpkg.Dir(path)), pathpkg.Base(path)
}

func appendCopyStep(result *DriverCopyResult, phase string, bytes int64, start time.Time, err error) {
	step := TransferStep{Phase: phase}
	duration := timeutil.Now().Sub(start)
	step.Duration = duration.String()
	step.DurationMS = durationMillis(duration)
	if err != nil {
		step.OK = false
		step.Error = err.Error()
		step.ErrorCategory = drive.ErrorCategory(err)
	} else {
		step.OK = true
	}
	step.Bytes = bytes
	result.Steps = append(result.Steps, step)
}

func appendCopyTrace(result *DriverCopyResult, phase, mount, driver, path string, bytes int64, start time.Time, extra map[string]any) {
	if start.IsZero() {
		return
	}
	finished := timeutil.Now()
	duration := finished.Sub(start)
	event := TransferTraceEvent{
		OpID:       result.OpID,
		Kind:       "transfer",
		Phase:      phase,
		State:      "completed",
		Mount:      mount,
		Driver:     driver,
		Path:       path,
		Bytes:      bytes,
		Duration:   duration.String(),
		DurationMS: durationMillis(duration),
		StartedAt:  start,
		FinishedAt: finished,
		Extra:      extra,
	}
	if errValue, ok := extra["error"].(string); ok && errValue != "" {
		event.State = "failed"
		event.Error = errValue
		event.ErrorCategory = drive.ErrorCategoryMessage(errValue)
	}
	if bytes > 0 && duration > 0 {
		event.Throughput = int64(float64(bytes) / duration.Seconds())
	}
	result.Timeline = append(result.Timeline, event)
}

func (p *directCopyProgress) Phase(phase drive.UploadPhase) {
	now := timeutil.Now()
	if p.current != "" {
		if p.stageDurations == nil {
			p.stageDurations = map[string]time.Duration{}
		}
		p.stageDurations[string(p.current)] += now.Sub(p.currentStarted)
	}
	p.current = phase
	p.currentStarted = now
}

func (p *directCopyProgress) Uploaded(n int64) {
	if n > 0 {
		p.bytesUploaded += n
	}
}

func (p *directCopyProgress) finish() {
	if p.current == "" {
		return
	}
	now := timeutil.Now()
	if p.stageDurations == nil {
		p.stageDurations = map[string]time.Duration{}
	}
	p.stageDurations[string(p.current)] += now.Sub(p.currentStarted)
	p.current = ""
}

func (p *directCopyProgress) stageDurationStrings() map[string]string {
	if len(p.stageDurations) == 0 {
		return nil
	}
	keys := make([]string, 0, len(p.stageDurations))
	for key := range p.stageDurations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = p.stageDurations[key].String()
	}
	return out
}
