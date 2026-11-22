package control

import (
	"context"
	"strings"
	"testing"
)

// TestDebugServerDoesNotRegisterMutableRoutes pins the read-only contract of
// the debug server: mutable handlers are registered only by the probe/command
// server. This is the route-level counterpart to the CLI help split.
func TestDebugServerDoesNotRegisterMutableRoutes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	debug, err := NewServer(testEndpoint(t), fakeSnapshotter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := debug.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer debug.Close(context.Background())
	debugClient, err := NewClient(debug.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/debug/cache/clear?mount=cloud", "/v1/bench", "/v1/driver/test"} {
		if _, err := debugClient.PostJSON(ctx, path, map[string]string{}); err == nil ||
			!strings.Contains(err.Error(), "status 404") {
			t.Fatalf("debug server exposed mutable route %s: %v", path, err)
		}
	}

	probe, err := NewProbeServer(testEndpoint(t), fakeSnapshotter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer probe.Close(context.Background())
	probeClient, err := NewClient(probe.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probeClient.PostJSON(ctx, "/v1/debug/cache/clear?mount=cloud", map[string]string{}); err != nil &&
		strings.Contains(err.Error(), "status 404") {
		t.Fatalf("probe server did not register cache clear: %v", err)
	}
}
