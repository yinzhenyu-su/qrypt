package drivecopy

import (
	"bytes"
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"io"
	pathpkg "path"
	"strings"
	"testing"
	"time"
)

// Namespace must satisfy the full driver-copy source surface (resolve +
// snapshot), which CLI copy/sync depends on.
var _ diagnostics.DriverCopySource = (*vfs.Namespace)(nil)

type directCopyTestSource struct {
	drivers  []diagnostics.NamedDriver
	resolves map[string]diagnostics.DebugResolveInfo
}

func (s directCopyTestSource) Drivers() []diagnostics.NamedDriver {
	return s.drivers
}

func (s directCopyTestSource) DebugResolve(_ context.Context, p string, _ bool) (diagnostics.DebugResolveInfo, error) {
	p = vfs.CleanVirtualPath(p)
	if info, ok := s.resolves[p]; ok {
		info.Path = p
		if info.Parent == "" {
			info.Parent = vfs.CleanVirtualPath(pathpkg.Dir(p))
		}
		if info.PlainName == "" {
			info.PlainName = pathpkg.Base(p)
		}
		return info, nil
	}
	return diagnostics.DebugResolveInfo{
		Path:      p,
		Parent:    vfs.CleanVirtualPath(pathpkg.Dir(p)),
		PlainName: pathpkg.Base(p),
		Mount:     firstPathSegment(p),
	}, nil
}

func (s directCopyTestSource) DebugSnapshot() diagnostics.DebugSnapshot {
	return diagnostics.DebugSnapshot{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		Kind:          "namespace",
		Mounts: []diagnostics.MountSnapshot{
			{Identity: diagnostics.MountSnapshotIdentity{Name: "src", DriverName: "src-test"}},
			{Identity: diagnostics.MountSnapshotIdentity{Name: "dst", DriverName: "dst-test"}},
		},
	}
}

type directCopyTestDriver struct {
	drive.UnsupportedOperations
	driverName string
	files      map[string][]byte
	removed    []string
}

func (d *directCopyTestDriver) Init(context.Context) error { return nil }
func (d *directCopyTestDriver) Drop(context.Context) error { return nil }
func (d *directCopyTestDriver) List(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}
func (d *directCopyTestDriver) Read(_ context.Context, entry drive.Entry, _, _ int64) (io.ReadCloser, error) {
	data, ok := d.files[entry.ID]
	if !ok {
		return nil, fmt.Errorf("missing file %s", entry.ID)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (d *directCopyTestDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *directCopyTestDriver) Mkdir(context.Context, string, string) (drive.Entry, error) {
	return drive.Entry{}, nil
}
func (d *directCopyTestDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *directCopyTestDriver) Rename(context.Context, drive.Entry, string) error {
	return nil
}
func (d *directCopyTestDriver) Remove(_ context.Context, entry drive.Entry) error {
	d.removed = append(d.removed, entry.ID)
	delete(d.files, entry.ID)
	return nil
}
func (d *directCopyTestDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	time.Sleep(time.Millisecond)
	body, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	defer body.Close()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)
	data, err := io.ReadAll(body)
	if err != nil {
		return drive.Entry{}, err
	}
	drive.ReportUploadProgress(req.Progress, int64(len(data)))
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
	id := req.ParentID + "/" + req.Name
	d.files[id] = data
	return drive.Entry{ID: id, ParentID: req.ParentID, Name: req.Name, Size: int64(len(data))}, nil
}
func (d *directCopyTestDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{Driver: d.driverName}, nil
}

func (d *directCopyTestDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilitySourceUploader, drive.CapabilityWriter}
}

// Copy implements drive.ServerSideCopier for the same-driver routing test.
func (d *directCopyTestDriver) Copy(_ context.Context, src drive.Entry, dstParentID, dstName string) (drive.Entry, error) {
	data, ok := d.files[src.ID]
	if !ok {
		return drive.Entry{}, fmt.Errorf("missing file %s", src.ID)
	}
	id := dstParentID + "/" + dstName
	d.files[id] = append([]byte(nil), data...)
	return drive.Entry{ID: id, ParentID: dstParentID, Name: dstName, Size: int64(len(data))}, nil
}

func (d *directCopyTestDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

func TestRunDirectDriverCopyCopiesViaDrivers(t *testing.T) {
	srcDriver := &directCopyTestDriver{driverName: "src-test", files: map[string][]byte{"src-file": []byte("payload")}}
	dstDriver := &directCopyTestDriver{driverName: "dst-test", files: map[string][]byte{}}
	source := directCopyFixture(srcDriver, dstDriver, nil)

	result := RunDirectDriverCopy(context.Background(), source, "/src/file.bin", "/dst/copied.bin", false)
	if !result.Pass {
		t.Fatalf("copy pass = false, steps=%#v", result.Steps)
	}
	if result.OpID == "" {
		t.Fatal("copy result missing op_id")
	}
	for _, step := range result.Steps {
		if strings.HasPrefix(step.Duration, "-") {
			t.Fatalf("step %s duration = %q, want non-negative", step.Phase, step.Duration)
		}
	}
	if got := string(dstDriver.files["dst-root/copied.bin"]); got != "payload" {
		t.Fatalf("copied payload = %q, want payload", got)
	}
	if result.Bytes != int64(len("payload")) {
		t.Fatalf("bytes = %d, want %d", result.Bytes, len("payload"))
	}
	if !hasCopyEvent(result, "read_source_to_temp") || !hasCopyEvent(result, "driver_put_source") {
		t.Fatalf("timeline missing expected phases: %#v", result.Timeline)
	}
	driverEvent := findCopyEvent(result, "driver_put_source")
	if driverEvent.OpID != result.OpID || driverEvent.Kind != "transfer" {
		t.Fatalf("driver event missing unified operation metadata: %#v", driverEvent)
	}
	if driverEvent.Extra["bytes_uploaded"] != int64(len("payload")) {
		t.Fatalf("bytes_uploaded = %#v, want %d", driverEvent.Extra["bytes_uploaded"], len("payload"))
	}
	if driverEvent.Extra["stage_durations"] == nil {
		t.Fatalf("driver_put_source missing stage_durations: %#v", driverEvent)
	}
}

func TestRunDirectDriverCopyRequiresOverwriteForExistingDest(t *testing.T) {
	srcDriver := &directCopyTestDriver{driverName: "src-test", files: map[string][]byte{"src-file": []byte("new")}}
	dstDriver := &directCopyTestDriver{driverName: "dst-test", files: map[string][]byte{"existing": []byte("old")}}
	existing := &diagnostics.DebugResolveInfo{
		Mount:     "dst",
		Driver:    "dst-test",
		RemoteID:  "existing",
		ParentID:  "dst-root",
		PlainName: "copied.bin",
		Size:      3,
	}
	source := directCopyFixture(srcDriver, dstDriver, existing)

	blocked := RunDirectDriverCopy(context.Background(), source, "/src/file.bin", "/dst/copied.bin", false)
	if blocked.Pass {
		t.Fatal("copy without overwrite passed, want failure")
	}
	if got := string(dstDriver.files["existing"]); got != "old" {
		t.Fatalf("existing payload changed without overwrite: %q", got)
	}

	copied := RunDirectDriverCopy(context.Background(), source, "/src/file.bin", "/dst/copied.bin", true)
	if !copied.Pass {
		t.Fatalf("copy with overwrite pass = false, steps=%#v", copied.Steps)
	}
	if len(dstDriver.removed) != 1 || dstDriver.removed[0] != "existing" {
		t.Fatalf("removed = %#v, want [existing]", dstDriver.removed)
	}
	if got := string(dstDriver.files["dst-root/copied.bin"]); got != "new" {
		t.Fatalf("overwritten payload = %q, want new", got)
	}
}

func TestRunDirectDriverCopyDirReportsEntries(t *testing.T) {
	srcDriver := &directCopyTestDriver{driverName: "src-test", files: map[string][]byte{"src-file": []byte("payload")}}
	dstDriver := &directCopyTestDriver{driverName: "dst-test", files: map[string][]byte{"existing": []byte("old")}}
	fs := &directCopyDirFS{
		entries: map[string]drive.Entry{
			"/src/parent":          {Name: "parent", IsDir: true},
			"/src/parent/a.txt":    {Name: "a.txt", Size: int64(len("payload"))},
			"/src/parent/skip.txt": {Name: "skip.txt", Size: int64(len("old"))},
			"/dst":                 {Name: "dst", IsDir: true},
			"/dst/parent":          {Name: "parent", IsDir: true},
			"/dst/parent/skip.txt": {Name: "skip.txt", Size: int64(len("old"))},
		},
		lists: map[string][]drive.Entry{
			"/src/parent": {
				{Name: "a.txt", Size: int64(len("payload"))},
				{Name: "skip.txt", Size: int64(len("old"))},
			},
		},
	}
	source := directCopyTestSource{
		drivers: []diagnostics.NamedDriver{
			{Name: "src", Driver: srcDriver},
			{Name: "dst", Driver: dstDriver},
		},
		resolves: map[string]diagnostics.DebugResolveInfo{
			"/src/parent/a.txt": {
				Mount: "src", Driver: "src-test", RemoteID: "src-file", ParentID: "src-parent", PlainName: "a.txt", Size: int64(len("payload")),
			},
			"/src/parent/skip.txt": {
				Mount: "src", Driver: "src-test", RemoteID: "src-skip", ParentID: "src-parent", PlainName: "skip.txt", Size: int64(len("old")),
			},
			"/dst": {
				Mount: "dst", Driver: "dst-test", RemoteID: "dst-root", PlainName: "dst", IsDir: true,
			},
			"/dst/parent": {
				Mount: "dst", Driver: "dst-test", RemoteID: "dst-parent", ParentID: "dst-root", PlainName: "parent", IsDir: true,
			},
			"/dst/parent/skip.txt": {
				Mount: "dst", Driver: "dst-test", RemoteID: "existing", ParentID: "dst-parent", PlainName: "skip.txt", Size: int64(len("old")),
			},
		},
	}

	result := RunDirectDriverCopyDir(context.Background(), fs, source, "/src/parent", "/dst", false)
	if !result.Pass || result.OpID == "" || result.Copied != 1 || result.Skipped != 1 || result.Failed != 0 {
		t.Fatalf("unexpected copy dir result: %+v", result)
	}
	if got := string(dstDriver.files["dst-parent/a.txt"]); got != "payload" {
		t.Fatalf("copied payload = %q, want payload", got)
	}
	seen := map[string]string{}
	for _, entry := range result.Entries {
		seen[entry.Kind+":"+entry.SourcePath] = entry.State
	}
	for key, want := range map[string]string{
		"directory:/src/parent":     "ready",
		"file:/src/parent/a.txt":    "copied",
		"file:/src/parent/skip.txt": "skipped",
	} {
		if seen[key] != want {
			t.Fatalf("entry %s state = %q, want %q; entries=%+v", key, seen[key], want, result.Entries)
		}
	}
}

func TestRunDirectDriverCopyDirReportsFailedEntry(t *testing.T) {
	srcDriver := &directCopyTestDriver{driverName: "src-test", files: map[string][]byte{}}
	dstDriver := &directCopyTestDriver{driverName: "dst-test", files: map[string][]byte{}}
	fs := &directCopyDirFS{
		entries: map[string]drive.Entry{
			"/src/parent": {Name: "parent", IsDir: true},
			"/dst":        {Name: "dst", IsDir: true},
		},
		lists: map[string][]drive.Entry{
			"/src/parent": {{Name: "missing.txt", Size: 7}},
		},
	}
	source := directCopyTestSource{
		drivers: []diagnostics.NamedDriver{
			{Name: "src", Driver: srcDriver},
			{Name: "dst", Driver: dstDriver},
		},
		resolves: map[string]diagnostics.DebugResolveInfo{
			"/src/parent/missing.txt": {
				Mount: "src", Driver: "src-test", ParentID: "src-parent", PlainName: "missing.txt",
			},
			"/dst": {
				Mount: "dst", Driver: "dst-test", RemoteID: "dst-root", PlainName: "dst", IsDir: true,
			},
			"/dst/parent": {
				Mount: "dst", Driver: "dst-test", RemoteID: "dst-parent", ParentID: "dst-root", PlainName: "parent", IsDir: true,
			},
		},
	}

	result := RunDirectDriverCopyDir(context.Background(), fs, source, "/src/parent", "/dst", false)
	if result.Pass || result.Failed != 1 || result.Error == "" {
		t.Fatalf("unexpected failed copy dir result: %+v", result)
	}
	var failed DriverCopyEntryResult
	for _, entry := range result.Entries {
		if entry.State == "failed" {
			failed = entry
			break
		}
	}
	if failed.SourcePath != "/src/parent/missing.txt" || !strings.Contains(failed.Error, "source not found") {
		t.Fatalf("failed entry = %+v, want source not found for missing file", failed)
	}
}

type directCopyDirFS struct {
	entries map[string]drive.Entry
	lists   map[string][]drive.Entry
	mkdirs  []string
}

func (f *directCopyDirFS) Stat(_ context.Context, path string) (drive.Entry, error) {
	path = vfs.CleanVirtualPath(path)
	if entry, ok := f.entries[path]; ok {
		return entry, nil
	}
	return drive.Entry{}, vfs.ErrNotFound
}

func (f *directCopyDirFS) List(_ context.Context, path string) ([]drive.Entry, error) {
	path = vfs.CleanVirtualPath(path)
	if entries, ok := f.lists[path]; ok {
		return entries, nil
	}
	return nil, vfs.ErrNotFound
}

func (f *directCopyDirFS) Mkdir(_ context.Context, path string) (drive.Entry, error) {
	path = vfs.CleanVirtualPath(path)
	f.mkdirs = append(f.mkdirs, path)
	entry := drive.Entry{Name: pathpkg.Base(path), IsDir: true}
	f.entries[path] = entry
	return entry, nil
}

func directCopyFixture(srcDriver, dstDriver *directCopyTestDriver, existingDest *diagnostics.DebugResolveInfo) directCopyTestSource {
	resolves := map[string]diagnostics.DebugResolveInfo{
		"/src/file.bin": {
			Mount:     "src",
			Driver:    "src-test",
			RemoteID:  "src-file",
			ParentID:  "src-root",
			PlainName: "file.bin",
			Size:      int64(len(srcDriver.files["src-file"])),
		},
		"/dst": {
			Mount:     "dst",
			Driver:    "dst-test",
			RemoteID:  "dst-root",
			PlainName: "dst",
			IsDir:     true,
		},
	}
	if existingDest != nil {
		resolves["/dst/copied.bin"] = *existingDest
	}
	return directCopyTestSource{
		drivers: []diagnostics.NamedDriver{
			{Name: "src", Driver: srcDriver},
			{Name: "dst", Driver: dstDriver},
		},
		resolves: resolves,
	}
}

func firstPathSegment(path string) string {
	path = strings.Trim(strings.TrimPrefix(vfs.CleanVirtualPath(path), "/"), "/")
	name, _, _ := strings.Cut(path, "/")
	return name
}

func hasCopyEvent(result *DriverCopyResult, phase string) bool {
	return findCopyEvent(result, phase).Phase != ""
}

func findCopyEvent(result *DriverCopyResult, phase string) drive.MetricEvent {
	for _, event := range result.Timeline {
		if event.Phase == phase {
			return event
		}
	}
	return drive.MetricEvent{}
}

// TestRunDirectDriverCopyServerSideSameDriver: when source and destination
// resolve to the same driver instance and it implements ServerSideCopier,
// the copy routes through the provider-side step and skips the
// download-to-temp-and-reupload round trip.
func TestRunDirectDriverCopyServerSideSameDriver(t *testing.T) {
	shared := &directCopyTestDriver{driverName: "shared-test", files: map[string][]byte{"src-file": []byte("payload")}}
	source := directCopyFixture(shared, shared, nil)

	result := RunDirectDriverCopy(context.Background(), source, "/src/file.bin", "/dst/copied.bin", false)
	if !result.Pass {
		t.Fatalf("server-side copy pass = false, steps=%#v", result.Steps)
	}
	if !hasCopyStep(result, "server_side_copy") {
		t.Fatalf("expected server_side_copy step, got %#v", result.Steps)
	}
	if hasCopyStep(result, "read_source_to_temp") || hasCopyStep(result, "driver_put_source") {
		t.Fatalf("server-side copy must not download to temp or re-upload: %#v", result.Steps)
	}
	if got := string(shared.files["dst-root/copied.bin"]); got != "payload" {
		t.Fatalf("copied payload = %q, want payload", got)
	}
	if result.Bytes != int64(len("payload")) {
		t.Fatalf("bytes = %d, want %d", result.Bytes, len("payload"))
	}
}

// TestRunDirectDriverCopyCrossDriverStillTemp: distinct driver instances
// fall back to the download-to-temp path even when both implement
// ServerSideCopier (a provider-side copy only exists within one backend).
func TestRunDirectDriverCopyCrossDriverStillTemp(t *testing.T) {
	srcDriver := &directCopyTestDriver{driverName: "src-test", files: map[string][]byte{"src-file": []byte("payload")}}
	dstDriver := &directCopyTestDriver{driverName: "dst-test", files: map[string][]byte{}}
	source := directCopyFixture(srcDriver, dstDriver, nil)

	result := RunDirectDriverCopy(context.Background(), source, "/src/file.bin", "/dst/copied.bin", false)
	if !result.Pass {
		t.Fatalf("cross-driver copy pass = false, steps=%#v", result.Steps)
	}
	if hasCopyStep(result, "server_side_copy") {
		t.Fatalf("cross-driver copy must not use server_side_copy: %#v", result.Steps)
	}
	if !hasCopyStep(result, "read_source_to_temp") || !hasCopyStep(result, "driver_put_source") {
		t.Fatalf("cross-driver copy missing temp round trip: %#v", result.Steps)
	}
}

func hasCopyStep(result *DriverCopyResult, phase string) bool {
	for _, step := range result.Steps {
		if step.Phase == phase {
			return true
		}
	}
	return false
}
