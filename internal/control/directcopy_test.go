package control

import (
	"bytes"
	"context"
	"fmt"
	"io"
	pathpkg "path"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type directCopyTestSource struct {
	drivers  []vfs.NamedDriver
	resolves map[string]vfs.DebugResolveInfo
}

func (s directCopyTestSource) Drivers() []vfs.NamedDriver {
	return s.drivers
}

func (s directCopyTestSource) DebugResolve(_ context.Context, p string, _ bool) (vfs.DebugResolveInfo, error) {
	p = cleanVirtual(p)
	if info, ok := s.resolves[p]; ok {
		info.Path = p
		if info.Parent == "" {
			info.Parent = cleanVirtual(pathpkg.Dir(p))
		}
		if info.PlainName == "" {
			info.PlainName = pathpkg.Base(p)
		}
		return info, nil
	}
	return vfs.DebugResolveInfo{
		Path:      p,
		Parent:    cleanVirtual(pathpkg.Dir(p)),
		PlainName: pathpkg.Base(p),
		Mount:     firstPathSegment(p),
	}, nil
}

func (s directCopyTestSource) DebugSnapshot() vfs.DebugSnapshot {
	return vfs.DebugSnapshot{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		Kind:          "namespace",
		Mounts: []vfs.DebugMountSnapshot{
			{Name: "src", DriverName: "src-test"},
			{Name: "dst", DriverName: "dst-test"},
		},
	}
}

type directCopyTestDriver struct {
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
	if !hasCopyTrace(result, "read_source_to_temp") || !hasCopyTrace(result, "driver_put_source") {
		t.Fatalf("timeline missing expected phases: %#v", result.Timeline)
	}
	driverTrace := findCopyTrace(result, "driver_put_source")
	if driverTrace.OpID != result.OpID || driverTrace.Kind != "transfer" {
		t.Fatalf("driver trace missing unified operation metadata: %#v", driverTrace)
	}
	if driverTrace.Extra["bytes_uploaded"] != int64(len("payload")) {
		t.Fatalf("bytes_uploaded = %#v, want %d", driverTrace.Extra["bytes_uploaded"], len("payload"))
	}
	if driverTrace.Extra["stage_durations"] == nil {
		t.Fatalf("driver_put_source missing stage_durations: %#v", driverTrace)
	}
}

func TestRunDirectDriverCopyRequiresOverwriteForExistingDest(t *testing.T) {
	srcDriver := &directCopyTestDriver{driverName: "src-test", files: map[string][]byte{"src-file": []byte("new")}}
	dstDriver := &directCopyTestDriver{driverName: "dst-test", files: map[string][]byte{"existing": []byte("old")}}
	existing := &vfs.DebugResolveInfo{
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

func directCopyFixture(srcDriver, dstDriver *directCopyTestDriver, existingDest *vfs.DebugResolveInfo) directCopyTestSource {
	resolves := map[string]vfs.DebugResolveInfo{
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
		drivers: []vfs.NamedDriver{
			{Name: "src", Driver: srcDriver},
			{Name: "dst", Driver: dstDriver},
		},
		resolves: resolves,
	}
}

func firstPathSegment(path string) string {
	path = strings.Trim(strings.TrimPrefix(cleanVirtual(path), "/"), "/")
	name, _, _ := strings.Cut(path, "/")
	return name
}

func hasCopyTrace(result *DriverCopyResult, phase string) bool {
	return findCopyTrace(result, phase).Phase != ""
}

func findCopyTrace(result *DriverCopyResult, phase string) TransferTraceEvent {
	for _, event := range result.Timeline {
		if event.Phase == phase {
			return event
		}
	}
	return TransferTraceEvent{}
}
