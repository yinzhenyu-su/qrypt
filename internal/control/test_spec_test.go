package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
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

// TestDriverTestSpecsRegistered asserts every spec the endpoint accepts has
// a coherent registration (identity, capability prerequisites, runner).
func TestDriverTestSpecsRegistered(t *testing.T) {
	for _, name := range []string{"auth", "crud", "instantupload", "multipart"} {
		spec, ok := driverTestSpecs[name]
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
	d := &noUploaderDriver{crudMemoryDriver: *newCRUDMemoryDriver()}
	snapshotter := fakeSnapshotter{drivers: []vfs.NamedDriver{{
		Name:        "local",
		Driver:      d,
		TestEnabled: true,
	}}}
	_, client := newTestServer(t, snapshotter)

	// Explicit mount: coded failure run, not an HTTP error.
	body, err := client.PostJSON(context.Background(), "/v1/driver/test", DriverTestRequest{Test: "crud", Mount: "local"})
	if err != nil {
		t.Fatal(err)
	}
	var runs []TestRun
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
	body, err = client.PostJSON(context.Background(), "/v1/driver/test", DriverTestRequest{Test: "crud"})
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
	driver := newCRUDMemoryDriver()
	snapshotter := fakeSnapshotter{drivers: []vfs.NamedDriver{{
		Name:        "local",
		Driver:      driver,
		TestEnabled: true,
	}}}
	_, client := newTestServer(t, snapshotter)

	// crud, multipart, and auth run against the full memory driver.
	for _, spec := range []string{"crud", "multipart", "auth"} {
		body, err := client.PostJSON(context.Background(), "/v1/driver/test", DriverTestRequest{Test: spec, Mount: "local", Size: "64k"})
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		var runs []TestRun
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
	body, err := client.PostJSON(context.Background(), "/v1/driver/test", DriverTestRequest{Test: "auth", Mount: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"capabilities"`) {
		t.Fatalf("auth run missing capability matrix: %s", body)
	}
	_ = drive.CapabilityWriter

	// instantupload requires a driver reporting the instant-upload counter.
	instant := &instantUploadTestDriver{rootID: "root", reportCounter: true}
	instantSnapshotter := fakeSnapshotter{drivers: []vfs.NamedDriver{{
		Name:        "local",
		Driver:      instant,
		TestEnabled: true,
	}}}
	_, instantClient := newTestServer(t, instantSnapshotter)
	body, err = instantClient.PostJSON(context.Background(), "/v1/driver/test", DriverTestRequest{Test: "instantupload", Mount: "local"})
	if err != nil {
		t.Fatal(err)
	}
	var runs []TestRun
	if err := json.Unmarshal(body, &runs); err != nil {
		t.Fatalf("instantupload: unmarshal: %v body=%s", err, body)
	}
	if len(runs) != 1 || !runs[0].Pass || runs[0].Spec != "instantupload" {
		t.Fatalf("instantupload run failed: %+v", runs)
	}
}

// TestTestRunJSONStable verifies the unified envelope serializes with the
// documented field names (stable for storage/alerting consumers).
func TestTestRunJSONStable(t *testing.T) {
	tr := TestRun{
		Spec:     "crud",
		Mount:    "local",
		Pass:     true,
		Steps:    []TestStep{{Operation: "mkdir", OK: true, Duration: "1s", DurationMS: 1000}},
		Duration: "1s", DurationMS: 1000,
	}
	body, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"spec"`, `"mount"`, `"pass"`, `"steps"`, `"started_at"`, `"duration_ms"`, `"operation"`, `"ok"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("TestRun JSON missing %s: %s", want, body)
		}
	}
}
