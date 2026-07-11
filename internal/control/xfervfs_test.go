package control

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type xferFakeFS struct {
	files             map[string][]byte
	pending           []vfs.PendingFile
	corruptSourceRead bool
	removeCalls       []string
	removeDirCalls    []string
}

func newXferFakeFS() *xferFakeFS {
	return &xferFakeFS{files: map[string][]byte{}}
}

func (f *xferFakeFS) Start(context.Context) {}

func (f *xferFakeFS) Stat(context.Context, string) (drive.Entry, error) {
	return drive.Entry{}, nil
}

func (f *xferFakeFS) List(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}

func (f *xferFakeFS) Read(_ context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	data, ok := f.files[path]
	if !ok {
		return nil, vfs.ErrNotFound
	}
	out := append([]byte(nil), data...)
	if f.corruptSourceRead && strings.HasPrefix(path, "/src/") && len(out) > 0 {
		out[0] ^= 0xff
	}
	if offset > int64(len(out)) {
		offset = int64(len(out))
	}
	out = out[offset:]
	if size > 0 && size < int64(len(out)) {
		out = out[:size]
	}
	return io.NopCloser(bytes.NewReader(out)), nil
}

func (f *xferFakeFS) Create(_ context.Context, path string) error {
	f.files[path] = nil
	return nil
}

func (f *xferFakeFS) WriteAt(_ context.Context, path string, data []byte, off int64) (int, error) {
	file := f.files[path]
	end := int(off) + len(data)
	if end > len(file) {
		next := make([]byte, end)
		copy(next, file)
		file = next
	}
	copy(file[off:], data)
	f.files[path] = file
	return len(data), nil
}

func (f *xferFakeFS) Flush(context.Context, string) error {
	return nil
}

func (f *xferFakeFS) Mkdir(_ context.Context, path string) (drive.Entry, error) {
	return drive.Entry{ID: path, Name: path, IsDir: true}, nil
}

func (f *xferFakeFS) Remove(_ context.Context, path string) error {
	f.removeCalls = append(f.removeCalls, path)
	return nil
}

func (f *xferFakeFS) RemoveDir(_ context.Context, path string) error {
	f.removeDirCalls = append(f.removeDirCalls, path)
	return nil
}

func (f *xferFakeFS) Rename(context.Context, string, string) error {
	return nil
}

func (f *xferFakeFS) Truncate(context.Context, string, int64) error {
	return nil
}

func (f *xferFakeFS) Pending() []vfs.PendingFile {
	return f.pending
}

func (f *xferFakeFS) DebugSnapshot() vfs.DebugSnapshot {
	snapshot := vfs.DebugSnapshot{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		Kind:          "namespace",
		Mounts: []vfs.DebugMountSnapshot{
			{Name: "src", DriverName: "source-test"},
			{Name: "dst", DriverName: "dest-test"},
		},
	}
	started := time.Unix(100, 0)
	for path, data := range f.files {
		for i := range snapshot.Mounts {
			mount := snapshot.Mounts[i].Name
			prefix := "/" + mount
			if path != prefix && !strings.HasPrefix(path, prefix+"/") {
				continue
			}
			localPath := strings.TrimPrefix(path, prefix)
			if localPath == "" {
				localPath = "/"
			}
			bytesTotal := int64(len(data))
			snapshot.Mounts[i].UploadHistory = append(snapshot.Mounts[i].UploadHistory, vfs.DebugUpload{
				Path:          localPath,
				State:         "completed",
				BytesTotal:    bytesTotal,
				BytesUploaded: bytesTotal,
				StartedAt:     started,
				CompletedAt:   started.Add(10 * time.Millisecond),
				UpdatedAt:     started.Add(10 * time.Millisecond),
				StageDurations: map[string]string{
					"uploading": "10ms",
				},
				Trace: []vfs.DebugTraceEvent{{
					Phase:      "driver_put_source",
					Bytes:      bytesTotal,
					Duration:   "10ms",
					Throughput: bytesTotal * 100,
					StartedAt:  started,
					FinishedAt: started.Add(10 * time.Millisecond),
				}},
			})
		}
	}
	return snapshot
}

func TestRunVFSXferTestFailsOnCorruptSourceRead(t *testing.T) {
	fs := newXferFakeFS()
	fs.corruptSourceRead = true

	result := RunVFSXferTest(context.Background(), fs, "src", "dst", 32)
	if result.Pass {
		t.Fatalf("RunVFSXferTest pass = true, want false")
	}
	for _, step := range result.Steps {
		if step.Phase == "read_source" {
			if step.OK {
				t.Fatalf("read_source OK = true, want false")
			}
			if !strings.Contains(step.Error, "source content mismatch") {
				t.Fatalf("read_source error = %q, want source content mismatch", step.Error)
			}
			return
		}
	}
	t.Fatal("read_source step not found")
}

func TestWaitVFSIdleReturnsContextError(t *testing.T) {
	fs := newXferFakeFS()
	fs.pending = []vfs.PendingFile{{Path: "/pending"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitVFSIdle(ctx, fs, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitVFSIdle error = %v, want context.Canceled", err)
	}
}

func TestCleanupPathsRemovesDirectory(t *testing.T) {
	fs := newXferFakeFS()

	cleanupPaths(context.Background(), fs, "/src/test")
	if len(fs.removeDirCalls) != 1 || fs.removeDirCalls[0] != "/src/test" {
		t.Fatalf("RemoveDir calls = %#v, want [/src/test]", fs.removeDirCalls)
	}
	if len(fs.removeCalls) != 0 {
		t.Fatalf("Remove calls = %#v, want none", fs.removeCalls)
	}
}

func TestRunVFSXferTestIncludesUploadTimeline(t *testing.T) {
	fs := newXferFakeFS()

	result := RunVFSXferTest(context.Background(), fs, "src", "dst", 32)
	if !result.Pass {
		t.Fatalf("RunVFSXferTest pass = false, steps=%#v", result.Steps)
	}
	if result.OpID == "" {
		t.Fatal("transfer result missing op_id")
	}

	seen := map[string]bool{}
	for _, event := range result.Timeline {
		if event.OpID != result.OpID || event.Kind == "" {
			t.Fatalf("timeline event missing unified operation metadata: %#v", event)
		}
		role, _ := event.Extra["role"].(string)
		seen[role+"."+event.Phase] = true
		if event.Phase == "driver_put_source" && event.Mount == "" {
			t.Fatalf("timeline event missing mount: %#v", event)
		}
		if event.Phase == "read_source" && (event.Kind != "read" || event.Mount != "src" || event.Bytes != 32) {
			t.Fatalf("read timeline event mismatch: %#v", event)
		}
	}
	for _, key := range []string{
		"source.read_source",
		"source.upload_total",
		"source.driver_put_source",
		"dest.upload_total",
		"dest.driver_put_source",
	} {
		if !seen[key] {
			t.Fatalf("timeline missing %s: %#v", key, result.Timeline)
		}
	}
}
