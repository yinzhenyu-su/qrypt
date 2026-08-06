package vfs

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestReadSlotReleaseMatchesAcquiredSlot(t *testing.T) {
	fs := &VFS{
		read: &readState{
			slots: &readSlotState{
				normal: make(chan struct{}, 1),
				high:   make(chan struct{}, 1),
			},
		},
	}

	releaseHighNormalSlot, err := fs.acquireReadSlot(WithReadPriority(context.Background(), PriorityHigh))
	if err != nil {
		t.Fatal(err)
	}
	releaseHighReserveSlot, err := fs.acquireReadSlot(WithReadPriority(context.Background(), PriorityHigh))
	if err != nil {
		t.Fatal(err)
	}

	releaseHighNormalSlot()
	releaseNormalSlot, err := fs.acquireReadSlot(context.Background())
	if err != nil {
		t.Fatalf("normal read could not reuse released normal slot: %v", err)
	}
	defer releaseNormalSlot()

	releaseHighReserveSlot()
	releaseHighSlot, err := fs.acquireReadSlot(WithReadPriority(context.Background(), PriorityHigh))
	if err != nil {
		t.Fatalf("high-priority read could not reuse released high slot: %v", err)
	}
	defer releaseHighSlot()
}

func TestLoadWindowSlotFailurePropagatesToWaiter(t *testing.T) {
	fs, err := New(blockedReadSlotDriver{}, Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })
	for i := 0; i < cap(fs.read.slots.normal); i++ {
		fs.read.slots.normal <- struct{}{}
	}

	ctx := WithReadPriority(context.Background(), PriorityLow)
	entry := drive.Entry{ID: "file", Name: "file.bin", Size: readChunkSize, ModTime: time.Unix(1, 0)}
	if _, err := fs.loadWindow(ctx, entry, 0, 1); err == nil || err.Error() != "vfs: read slots full" {
		t.Fatalf("loadWindow err = %v, want read slots full", err)
	}
}

func TestWaitWindowTreatsCanceledLoadAsRetryableMiss(t *testing.T) {
	fs := &VFS{
		name: "test",
		read: &readState{
			history: newReadHistoryState(),
			windows: newReadWindowState(),
		},
		activeDebug: newActiveDebugState(),
	}
	load := &windowLoad{
		fid:  "file",
		done: make(chan struct{}),
		err:  context.Canceled,
	}
	close(load.done)
	fs.read.windows.loads["file\x000\x000"] = load

	data, ok, err := fs.waitWindow(context.Background(), "file", 0)
	if err != nil {
		t.Fatalf("waitWindow err = %v, want nil so caller retries canceled load", err)
	}
	if ok {
		t.Fatal("waitWindow ok = true, want false for canceled in-flight load")
	}
	if data != nil {
		t.Fatalf("waitWindow data len = %d, want nil", len(data))
	}
}

type blockedReadSlotDriver struct {
	drive.UnsupportedOperations
}

func (blockedReadSlotDriver) Init(context.Context) error { return nil }
func (blockedReadSlotDriver) Drop(context.Context) error { return nil }
func (blockedReadSlotDriver) List(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}
func (blockedReadSlotDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	panic("driver read should not run when read slot acquisition fails")
}
func (blockedReadSlotDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (blockedReadSlotDriver) Capabilities() []drive.Capability { return nil }
func (blockedReadSlotDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{}, nil
}
func (blockedReadSlotDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}
