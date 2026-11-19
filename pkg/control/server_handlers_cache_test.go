package control

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

// fakeCacheFillSource implements the snapshotter surface plus FileSystem, so
// the probe server can warm one file through the same read path a mount serves.
type fakeCacheFillSource struct {
	fakeReadCacheClearer
	content []byte
}

func (f *fakeCacheFillSource) DebugSnapshot() diagnostics.DebugSnapshot {
	snapshot := f.fakeReadCacheClearer.DebugSnapshot()
	if len(snapshot.Mounts) == 0 {
		snapshot.Mounts = []diagnostics.MountSnapshot{{
			Identity: diagnostics.MountSnapshotIdentity{Name: "cloud"},
			Cache:    diagnostics.DebugCacheSnapshot{Bytes: int64(len(f.content))},
		}}
	}
	return snapshot
}

func (f *fakeCacheFillSource) Stat(context.Context, string) (drive.Entry, error) {
	return drive.Entry{ID: "movie-id", Name: "movie.mp4", Size: int64(len(f.content))}, nil
}

func (f *fakeCacheFillSource) List(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}

func (f *fakeCacheFillSource) Read(_ context.Context, _ string, offset, size int64) (io.ReadCloser, error) {
	if offset < 0 || offset > int64(len(f.content)) {
		return nil, errors.New("offset out of range")
	}
	end := offset + size
	if end > int64(len(f.content)) {
		end = int64(len(f.content))
	}
	return io.NopCloser(bytes.NewReader(f.content[offset:end])), nil
}

func (f *fakeCacheFillSource) Create(context.Context, string) error { return errors.New("read-only") }
func (f *fakeCacheFillSource) WriteAt(context.Context, string, []byte, int64) (int, error) {
	return 0, errors.New("read-only")
}
func (f *fakeCacheFillSource) Flush(context.Context, string) error { return nil }
func (f *fakeCacheFillSource) Mkdir(context.Context, string) (drive.Entry, error) {
	return drive.Entry{}, errors.New("read-only")
}
func (f *fakeCacheFillSource) Remove(context.Context, string) error { return errors.New("read-only") }
func (f *fakeCacheFillSource) RemoveDir(context.Context, string) error {
	return errors.New("read-only")
}
func (f *fakeCacheFillSource) Rename(context.Context, string, string) error {
	return errors.New("read-only")
}
func (f *fakeCacheFillSource) Truncate(context.Context, string, int64) error {
	return errors.New("read-only")
}

func TestProbeServerFillsReadCacheThroughVFS(t *testing.T) {
	source := &fakeCacheFillSource{content: bytes.Repeat([]byte("x"), 3<<20)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probe, err := NewProbeServer(testEndpoint(t), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer probe.Close(context.Background())
	client, err := NewClient(probe.endpoint)
	if err != nil {
		t.Fatal(err)
	}

	body, err := client.PostJSON(ctx, "/v1/debug/cache/fill?mount=cloud&path=%2Fcloud%2Fmovie.mp4&bytes=1048576&block_size=262144", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"mount": "cloud"`,
		`"path": "/cloud/movie.mp4"`,
		`"bytes": 1048576`,
		`"block_size": 262144`,
		`"read_calls": 4`,
		`"file_size": 3145728`,
		`"schema_version"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("cache fill response missing %s: %s", want, body)
		}
	}

	// The mount-prefixed and mount-relative virtual paths both resolve.
	if _, err := client.PostJSON(ctx, "/v1/debug/cache/fill?mount=cloud&path=%2Fmovie.mp4&bytes=1", map[string]string{}); err != nil {
		t.Fatalf("mount-relative fill: %v", err)
	}
	if _, err := client.PostJSON(ctx, "/v1/debug/cache/fill?mount=cloud&path=%2Fcloud%2Fmovie.mp4&bytes=1", map[string]string{}); err != nil {
		t.Fatalf("mount-prefixed fill: %v", err)
	}

	if _, err := client.PostJSON(ctx, "/v1/debug/cache/fill?path=%2Fcloud%2Fmovie.mp4", map[string]string{}); err == nil ||
		!strings.Contains(err.Error(), "status 400") {
		t.Fatalf("fill without mount error = %v, want status 400", err)
	}
	if _, err := client.PostJSON(ctx, "/v1/debug/cache/fill?mount=cloud", map[string]string{}); err == nil ||
		!strings.Contains(err.Error(), "status 400") {
		t.Fatalf("fill without path error = %v, want status 400", err)
	}
	if _, err := client.Get(ctx, "/v1/debug/cache/fill?mount=cloud&path=%2Fcloud%2Fmovie.mp4"); err == nil ||
		!strings.Contains(err.Error(), "status 405") {
		t.Fatalf("GET fill error = %v, want status 405", err)
	}

	// A regular (debug) server must not expose a cache-mutating route.
	debugServer, err := NewServer(testEndpoint(t), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := debugServer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer debugServer.Close(context.Background())
	debugClient, err := NewClient(debugServer.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := debugClient.PostJSON(ctx, "/v1/debug/cache/fill?mount=cloud&path=%2Fcloud%2Fmovie.mp4", map[string]string{}); err == nil ||
		!strings.Contains(err.Error(), "status 404") {
		t.Fatalf("fill on debug socket error = %v, want status 404", err)
	}
}
