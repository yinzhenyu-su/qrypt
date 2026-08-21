package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/contracttest"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

// newTestServer boots a control server on a temp unix socket with the given
// snapshotter and returns an HTTP client bound to it.
func newTestServer(t *testing.T, source fakeSnapshotter) (*Server, *Client) {
	t.Helper()
	socketPath := testSocketPath(t)
	server, err := NewServer(socketPath, source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = server.Close(context.Background())
	})
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	return server, client
}

// capabilityMissingDriver exposes no capabilities so the scheduler routes
// it to coded failure / skip runs instead of executing the spec.
type capabilityMissingDriver struct {
	drive.UnsupportedOperations
}

func (capabilityMissingDriver) Init(context.Context) error { return nil }
func (capabilityMissingDriver) Drop(context.Context) error { return nil }
func (capabilityMissingDriver) List(context.Context, string) ([]drive.Entry, error) {
	return nil, drive.ErrUnsupported
}
func (capabilityMissingDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, drive.ErrUnsupported
}
func (capabilityMissingDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrUnsupported
}
func (capabilityMissingDriver) Capabilities() []drive.Capability { return nil }
func (capabilityMissingDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{Driver: "no-cap", Health: drive.HealthLevelOK}, nil
}
func (capabilityMissingDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

// instantUploadStubDriver reports the instant-upload counter in its debug
// snapshot, which is what the instantupload spec asserts on.
type instantUploadStubDriver struct {
	drive.UnsupportedOperations
	counter int64
	uploads int
}

func (d *instantUploadStubDriver) Init(context.Context) error { return nil }
func (d *instantUploadStubDriver) Drop(context.Context) error { return nil }
func (d *instantUploadStubDriver) List(context.Context, string) ([]drive.Entry, error) {
	return nil, fmt.Errorf("list should not be needed")
}
func (d *instantUploadStubDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (d *instantUploadStubDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *instantUploadStubDriver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	return drive.Entry{ID: "dir", ParentID: parentID, Name: name, IsDir: true, ModTime: time.Now()}, nil
}
func (d *instantUploadStubDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *instantUploadStubDriver) Rename(context.Context, drive.Entry, string) error {
	return nil
}
func (d *instantUploadStubDriver) Remove(context.Context, drive.Entry) error { return nil }
func (d *instantUploadStubDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	d.uploads++
	if d.uploads == 2 {
		d.counter++
	}
	return drive.Entry{ID: req.Name, ParentID: req.ParentID, Name: req.Name, Size: req.Source.Size()}, nil
}
func (d *instantUploadStubDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}
func (d *instantUploadStubDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{
		Driver:      "instant-upload-test",
		Health:      drive.HealthLevelOK,
		GeneratedAt: time.Now(),
		Extra:       map[string]any{drive.DebugExtraInstantUploadCount: d.counter},
	}, nil
}
func (d *instantUploadStubDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityPathResolver, drive.CapabilityWriter, drive.CapabilitySourceUploader}
}
func (d *instantUploadStubDriver) ResolvePath(context.Context, string) (string, error) {
	return "root", nil
}

// TestDriverTestSpecsRegistered asserts every spec the endpoint accepts has
// a coherent registration (identity, capability prerequisites, runner).
func TestDriverTestSpecsRegistered(t *testing.T) {
	for _, name := range []string{"auth", "crud", "instantupload", "multipart"} {
		spec, ok := contracttest.Specs()[name]
		if !ok {
			t.Fatalf("spec %q not registered", name)
		}
		if spec.Name != name || spec.Run == nil {
			t.Fatalf("spec %q incomplete: %+v", name, spec)
		}
	}
}

// TestDriverTestCapabilityMatrix verifies the scheduler filters mounts by the
// spec's required capabilities: explicit mounts get a coded failure run,
// bulk runs skip the mount with a reason.
func TestDriverTestCapabilityMatrix(t *testing.T) {
	d := &capabilityMissingDriver{}
	snapshotter := fakeSnapshotter{drivers: []diagnostics.NamedDriver{{
		Name:        "local",
		Driver:      d,
		TestEnabled: true,
	}}}
	_, client := newTestServer(t, snapshotter)

	// Explicit mount: coded failure run, not an HTTP error.
	body, err := client.PostJSON(context.Background(), "/v1/driver/test", contracttest.DriverTestRequest{Test: "crud", Mount: "local"})
	if err != nil {
		t.Fatal(err)
	}
	var runs []contracttest.TestRun
	if err := json.Unmarshal(body, &runs); err != nil {
		t.Fatalf("unmarshal runs: %v body=%s", err, body)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1: %s", len(runs), body)
	}
	r := runs[0]
	if r.Spec != "crud" || r.Mount != "local" || r.Pass {
		t.Fatalf("expected failing crud run, got %+v", r)
	}
	if r.Error == "" || r.ErrorCategory != "unsupported" {
		t.Fatalf("expected coded capability error, got %+v", r)
	}

	// Bulk run: capability-missing mounts are skipped with a reason.
	body, err = client.PostJSON(context.Background(), "/v1/driver/test", contracttest.DriverTestRequest{Test: "crud"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &runs); err != nil {
		t.Fatalf("unmarshal bulk runs: %v body=%s", err, body)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d bulk runs, want 1: %s", len(runs), body)
	}
	if !runs[0].Skipped || runs[0].SkipReason == "" {
		t.Fatalf("expected skipped run with reason, got %+v", runs[0])
	}
}

// TestDriverTestUnifiedEnvelope drives several specs through the real server
// and asserts the response is a unified []TestRun with stable fields, so
// consumers can parse one schema regardless of spec.
func TestDriverTestUnifiedEnvelope(t *testing.T) {
	driver := localfs.New(t.TempDir())
	if err := driver.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshotter := fakeSnapshotter{drivers: []diagnostics.NamedDriver{{
		Name:        "local",
		Driver:      driver,
		TestEnabled: true,
	}}}
	_, client := newTestServer(t, snapshotter)

	// multipart and auth run against the full driver; crud's rename
	// semantics assume stable object IDs (covered in contracttest against
	// the memory driver), so the envelope is validated via the other specs.
	for _, spec := range []string{"multipart", "auth"} {
		body, err := client.PostJSON(context.Background(), "/v1/driver/test", contracttest.DriverTestRequest{Test: spec, Mount: "local", Size: "64k"})
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		var runs []contracttest.TestRun
		if err := json.Unmarshal(body, &runs); err != nil {
			t.Fatalf("%s: unmarshal: %v body=%s", spec, err, body)
		}
		if len(runs) != 1 {
			t.Fatalf("%s: got %d runs, want 1", spec, len(runs))
		}
		r := runs[0]
		if r.Spec != spec || r.Mount != "local" {
			t.Fatalf("%s: run identity wrong: %+v", spec, r)
		}
		if !r.Pass {
			t.Fatalf("%s: run failed: %+v", spec, r)
		}
		if len(r.Steps) == 0 {
			t.Fatalf("%s: run has no steps", spec)
		}
		for _, step := range r.Steps {
			if step.Operation == "" || step.Duration == "" {
				t.Fatalf("%s: step missing unified fields: %+v", spec, step)
			}
		}
		if r.Started.IsZero() || r.Finished.IsZero() || r.Duration == "" || r.DurationMS <= 0 {
			t.Fatalf("%s: run missing timing fields: %+v", spec, r)
		}
	}

	// auth also reports the driver capability matrix.
	body, err := client.PostJSON(context.Background(), "/v1/driver/test", contracttest.DriverTestRequest{Test: "auth", Mount: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"capabilities"`) {
		t.Fatalf("auth run missing capability matrix: %s", body)
	}

	// instantupload requires a driver reporting the instant-upload counter.
	instant := &instantUploadStubDriver{}
	instantSnapshotter := fakeSnapshotter{drivers: []diagnostics.NamedDriver{{
		Name:        "local",
		Driver:      instant,
		TestEnabled: true,
	}}}
	_, instantClient := newTestServer(t, instantSnapshotter)
	body, err = instantClient.PostJSON(context.Background(), "/v1/driver/test", contracttest.DriverTestRequest{Test: "instantupload", Mount: "local"})
	if err != nil {
		t.Fatal(err)
	}
	var runs []contracttest.TestRun
	if err := json.Unmarshal(body, &runs); err != nil {
		t.Fatalf("instantupload: unmarshal: %v body=%s", err, body)
	}
	if len(runs) != 1 || !runs[0].Pass || runs[0].Spec != "instantupload" {
		t.Fatalf("instantupload run failed: %+v", runs)
	}
}

// TestVFSSpecsRegistered asserts the VFS-layer specs live in the
// same registry as drive-layer specs, with the RequiresVFS flag set.
func TestVFSSpecsRegistered(t *testing.T) {
	for _, name := range []string{"batchmove", "batchupload", "fs", "resume"} {
		spec, ok := contracttest.Specs()[name]
		if !ok {
			t.Fatalf("spec %q not registered", name)
		}
		if !spec.RequiresVFS {
			t.Fatalf("spec %q must require the VFS layer", name)
		}
	}
}

// TestVFSSpecRequiresFileSystem verifies the scheduler rejects VFS specs on a
// source that does not implement FileSystem (drive-layer snapshotters).
func TestVFSSpecRequiresFileSystem(t *testing.T) {
	driver := localfs.New(t.TempDir())
	if err := driver.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshotter := fakeSnapshotter{drivers: []diagnostics.NamedDriver{{
		Name:        "local",
		Driver:      driver,
		TestEnabled: true,
	}}}
	_, client := newTestServer(t, snapshotter) // fakeSnapshotter: no FileSystem
	if _, err := client.PostJSON(context.Background(), "/v1/driver/test", contracttest.DriverTestRequest{Test: "fs", Mount: "local"}); err == nil ||
		!strings.Contains(err.Error(), "does not implement FileSystem") {
		t.Fatalf("expected FileSystem rejection, got %v", err)
	}
}

// TestVFSSpecsRunThroughRegistry drives the VFS specs through the real
// server with a genuine VFS namespace (localfs mount) and asserts the
// unified envelope, including pass and residual-free cleanup.
func TestVFSSpecsRunThroughRegistry(t *testing.T) {
	root := t.TempDir()
	driver := localfs.New(root)
	if err := driver.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	v, err := vfs.New(driver, vfs.Options{
		StorageDir: filepath.Join(t.TempDir(), "cache"), TestEnabled: true,
		UploadDelay: time.Millisecond, DeleteDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "local", FS: v}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ns.Start(ctx)
	t.Cleanup(cancel)
	t.Cleanup(func() { stopVFS(t, v) })

	server, err := NewServer(testSocketPath(t), ns)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })

	client, err := NewClient(server.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	requests := []contracttest.DriverTestRequest{
		{Test: "fs", Mount: "local", Size: "4k"},
		{Test: "batchupload", Mount: "local", Count: 4, Size: "64"},
		{Test: "batchmove", Mount: "local", Count: 4, Size: "64"},
	}
	for _, req := range requests {
		body, err := client.PostJSON(context.Background(), "/v1/driver/test", req)
		if err != nil {
			t.Fatalf("%s: %v", req.Test, err)
		}
		var runs []contracttest.TestRun
		if err := json.Unmarshal(body, &runs); err != nil {
			t.Fatalf("%s: unmarshal: %v body=%s", req.Test, err, body)
		}
		if len(runs) != 1 {
			t.Fatalf("%s: got %d runs, want 1", req.Test, len(runs))
		}
		r := runs[0]
		if r.Spec != req.Test || r.Mount != "local" {
			t.Fatalf("%s: run identity wrong: %+v", req.Test, r)
		}
		if !r.Pass {
			t.Fatalf("%s run failed: %+v", req.Test, r)
		}
		if len(r.Steps) == 0 {
			t.Fatalf("%s run has no steps", req.Test)
		}
		if r.Started.IsZero() || r.Finished.IsZero() || r.Duration == "" {
			t.Fatalf("%s run missing timing: %+v", req.Test, r)
		}
		if strings.HasPrefix(req.Test, "batch") && len(r.Metrics) == 0 {
			t.Fatalf("%s run has no batch metrics", req.Test)
		}
	}
}

// stopVFS synchronously drops a VFS's cache files so a test TempDir cleanup
// never races the asynchronous shutdown (see pkg/vfs helpers).
func stopVFS(t *testing.T, fs vfs.FileSystem) {
	t.Helper()
	if closer, ok := fs.(interface{ CloseReadCache() error }); ok {
		if err := closer.CloseReadCache(); err != nil {
			t.Logf("close read cache: %v", err)
		}
	}
	if clearer, ok := fs.(interface{ ClearReadCache() error }); ok {
		if err := clearer.ClearReadCache(); err != nil {
			t.Logf("clear read cache: %v", err)
		}
	}
}
