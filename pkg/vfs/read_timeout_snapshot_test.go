package vfs

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

type snapshotStalledBody struct{ ctx context.Context }

func (b *snapshotStalledBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *snapshotStalledBody) Close() error { return nil }

// TestVFSDebugSnapshotRuntimeReportsReadTimeouts: the snapshot runtime must
// expose fetch timeouts (foreground and prefetch counted separately) and the
// window that timed out most recently.
func TestVFSDebugSnapshotRuntimeReportsReadTimeouts(t *testing.T) {
	t.Parallel()
	fake := drive.NewFakeDriver()
	if err := fake.Seed(map[string]string{"/file.bin": string(make([]byte, 4<<20))}); err != nil {
		t.Fatal(err)
	}
	fake.ReadBehavior.BodyFunc = func(ctx context.Context, _ []byte) io.ReadCloser {
		return &snapshotStalledBody{ctx: ctx}
	}
	fs, err := New(fake, Options{
		Name:       "cloud",
		StorageDir: t.TempDir(),
		ReadProfile: &drive.ReadProfile{
			FetchIdleTimeout:  50 * time.Millisecond,
			FetchTotalTimeout: 5 * time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fs.Start(ctx)
	defer func() { _ = fs.Close(ctx) }()

	if _, err := fs.Read(ctx, "/file.bin", 0, 1<<20); !drive.IsFetchTimeout(err) {
		t.Fatalf("read error = %v, want a fetch timeout", err)
	}

	runtime := newVFSDebugSnapshotRuntime(fs)
	state := runtime.Runtime()
	if state.ReadTimeouts == 0 {
		t.Fatal("runtime snapshot did not count the foreground read timeout")
	}
	if state.PrefetchTimeouts != 0 {
		t.Fatalf("prefetch timeouts = %d, want 0", state.PrefetchTimeouts)
	}
	if state.LastTimeout == nil {
		t.Fatal("runtime snapshot is missing the last timeout window")
	}
	if state.LastTimeout.Background {
		t.Fatal("last timeout reported as background, want foreground")
	}
	if state.LastTimeout.Kind != string(drive.FetchTimeoutIdle) {
		t.Fatalf("last timeout kind = %q, want idle", state.LastTimeout.Kind)
	}

	snapshot := diagnostics.AssembleMountSnapshot("cloud", runtime)
	if snapshot.Runtime.ReadTimeouts == 0 || snapshot.Runtime.LastTimeout == nil {
		t.Fatalf("assembled snapshot runtime = %+v, want timeout fields", snapshot.Runtime)
	}
	if snapshot.Runtime.LastTimeout.WindowStart != 0 || snapshot.Runtime.LastTimeout.WindowEnd < 0 {
		t.Fatalf("assembled last timeout = %+v, want the timed-out window", snapshot.Runtime.LastTimeout)
	}
	if snapshot.ReadProfile.FetchIdleTimeoutMS != 50 {
		t.Fatalf("profile fetch idle timeout = %d ms, want 50", snapshot.ReadProfile.FetchIdleTimeoutMS)
	}
}
