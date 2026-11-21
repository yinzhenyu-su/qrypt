package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/control"
)

// TestControlServerExposesProductSurface proves the command endpoint can serve
// capabilities, health, and the task journal, so no product client has to read
// the diagnostic endpoint.
func TestControlServerExposesProductSurface(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Mounts: []config.MountConfig{
		{Name: "local", Type: "localfs", Params: config.ParamMap{"root_path": remote}},
	}}
	fs, cleanup, err := BuildFileSystem(ctx, cfg, Options{Runtime: testRuntimeLayout(tmp)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	kernel := newTestCore(t, fs)

	// The unix socket path must stay short: t.TempDir already nests deeply.
	runDir, err := os.MkdirTemp("", "qrypt-ctl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	endpoint := filepath.Join(runDir, "control.sock")
	if err := kernel.StartControlServer(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	client, err := control.NewClient(endpoint)
	if err != nil {
		t.Fatal(err)
	}

	body, err := client.Get(ctx, "/v1/control/capabilities")
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	var capabilities control.CapabilitiesResponse
	if err := json.Unmarshal(body, &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.ProtocolVersion != control.ProtocolVersion {
		t.Fatalf("protocol version = %d", capabilities.ProtocolVersion)
	}
	features := map[string]bool{}
	for _, feature := range capabilities.Features {
		features[feature] = true
	}
	for _, want := range []string{"fs.read", "fs.write", "fs.list", "tasks.control", "tasks.list"} {
		if !features[want] {
			t.Fatalf("missing feature %q in %v", want, capabilities.Features)
		}
	}
	if len(capabilities.Mounts) != 1 || capabilities.Mounts[0].Name != "local" {
		t.Fatalf("mounts = %#v", capabilities.Mounts)
	}

	body, err = client.Get(ctx, "/v1/control/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	var health control.RuntimeHealth
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatal(err)
	}
	if health.Status == "" {
		t.Fatalf("health status missing: %s", body)
	}
	if len(health.Mounts) != 1 || health.Mounts[0].Mount != "local" {
		t.Fatalf("mount health = %#v", health.Mounts)
	}
	if health.Runtime == nil || health.Runtime.PID != os.Getpid() {
		t.Fatalf("runtime = %#v", health.Runtime)
	}

	body, err = client.Get(ctx, "/v1/control/tasks")
	if err != nil {
		t.Fatalf("tasks: %v", err)
	}
	var tasks control.TaskListResponse
	if err := json.Unmarshal(body, &tasks); err != nil {
		t.Fatal(err)
	}
	if tasks.Tasks == nil {
		t.Fatalf("tasks must be an empty array, got %s", body)
	}

	// A missing task must be a typed not_found from the client, not a raw body.
	_, err = client.Get(ctx, "/v1/control/tasks/missing")
	transport, ok := err.(*control.TransportError)
	if !ok {
		t.Fatalf("error = %v (%T)", err, err)
	}
	if transport.Code != control.CodeNotFound {
		t.Fatalf("code = %q, want %q", transport.Code, control.CodeNotFound)
	}
}
