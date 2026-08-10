package vfs

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// blockingSnapshotDriver blocks in DebugSnapshot until released, so tests
// can verify Namespace diagnostics never hold the namespace mutex while
// running per-mount queries.
type blockingSnapshotDriver struct {
	drive.UnsupportedOperations
	started chan struct{}
	block   chan struct{}
}

func (d *blockingSnapshotDriver) Init(context.Context) error { return nil }
func (d *blockingSnapshotDriver) Drop(context.Context) error { return nil }
func (d *blockingSnapshotDriver) List(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}
func (d *blockingSnapshotDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *blockingSnapshotDriver) Capabilities() []drive.Capability { return nil }
func (d *blockingSnapshotDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}
func (d *blockingSnapshotDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, nil
}
func (d *blockingSnapshotDriver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	close(d.started)
	select {
	case <-d.block:
	case <-ctx.Done():
	}
	return drive.DebugSnapshot{Driver: "blocking", GeneratedAt: time.Now()}, nil
}

// TestNamespaceDebugSnapshotDoesNotHoldLock: while a mount snapshot blocks
// in the driver, other Namespace operations must complete - the mount
// list is copied under the lock and queries run after releasing it.
func TestNamespaceDebugSnapshotDoesNotHoldLock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &blockingSnapshotDriver{started: make(chan struct{}), block: make(chan struct{})}
	mount, err := New(drv, Options{Name: "backend-a", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer mount.Close(ctx)
	ns, err := NewNamespace([]Mount{{Name: "photos", FS: mount}})
	if err != nil {
		t.Fatal(err)
	}

	snapDone := make(chan struct{})
	go func() {
		ns.DebugSnapshotForMounts([]string{"photos"})
		close(snapDone)
	}()

	// Wait until the snapshot is inside the (blocking) driver call.
	select {
	case <-drv.started:
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot never reached the driver")
	}

	// The namespace read lock must be free: a concurrent diagnostics
	// query completes immediately.
	otherDone := make(chan struct{})
	go func() {
		ns.Drivers()
		close(otherDone)
	}()
	select {
	case <-otherDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Drivers blocked by snapshot holding the namespace lock")
	}

	close(drv.block)
	select {
	case <-snapDone:
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot never completed after driver release")
	}
}
