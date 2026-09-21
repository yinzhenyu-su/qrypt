package core

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/read"
)

// --- Priority mapping through core ----------------------------------------

// priorityToVFS is the exact conversion ReadAtIntoWithPriority applies;
// locking it to the scheduler constants guarantees the mobile "high" and
// "normal" JSON options keep their scheduling meaning end to end.
func TestPriorityMappingMatchesVFSScheduler(t *testing.T) {
	cases := []struct {
		client ReadPriority
		want   vfs.ReadPriority
	}{
		{PriorityLow, vfs.PriorityLow},
		{PriorityNormal, vfs.PriorityNormal},
		{PriorityHigh, vfs.PriorityHigh},
	}
	for _, tt := range cases {
		if got := priorityToVFS(tt.client); got != tt.want {
			t.Fatalf("priorityToVFS(%d) = %v, want %v", tt.client, got, tt.want)
		}
	}
}

func TestReadAtIntoWithPriorityBehavesLikeReadAtInto(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := drive.NewFakeDriver()
	if err := raw.Seed(map[string]string{"f.bin": "0123456789abcdef" + strings.Repeat("z", 8192)}); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(raw, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	for _, priority := range []ReadPriority{PriorityNormal, PriorityHigh} {
		buf := make([]byte, 128)
		n, err := c.ReadAtIntoWithPriority(ctx, "/f.bin", 17, buf, 0, priority)
		if err != nil {
			t.Fatalf("ReadAtIntoWithPriority(%v) error: %v", priority, err)
		}
		plain := make([]byte, 128)
		n2, err := c.ReadAtInto(ctx, "/f.bin", 17, plain, 0)
		if err != nil {
			t.Fatalf("ReadAtInto error: %v", err)
		}
		if n != n2 || !bytes.Equal(buf[:n], plain[:n2]) {
			t.Fatalf("ReadAtIntoWithPriority(%v) = %q, plain ReadAtInto = %q", priority, buf[:n], plain[:n2])
		}
	}
}

func TestReadAtIntoWithPriorityRejectsInvalidPriority(t *testing.T) {
	for _, priority := range []ReadPriority{-1, 42} {
		_, err := (&Core{}).ReadAtIntoWithPriority(context.Background(), "/x", 0, make([]byte, 1), 0, priority)
		if err == nil {
			t.Fatalf("ReadAtIntoWithPriority(%d) accepted an invalid priority", priority)
		}
	}
}

// blockingReadProbe coordinates a driver whose Read parks every call until
// release is closed, so tests can saturate the VFS read slot pools.
type blockingReadProbe struct {
	// arrived is signaled once per Read call that reached the driver.
	arrived chan struct{}
	// release unblocks every parked Read once closed.
	release chan struct{}
}

// blockingReadDriver serves one flat file and parks every Read call,
// signaling arrival — the read-slot equivalent of a hung provider.
type blockingReadDriver struct {
	drive.UnsupportedOperations
	probe *blockingReadProbe
	size  int64
}

func (d *blockingReadDriver) Init(context.Context) error { return nil }
func (d *blockingReadDriver) Drop(context.Context) error { return nil }
func (d *blockingReadDriver) List(context.Context, string) ([]drive.Entry, error) {
	return []drive.Entry{{ID: "f1", ParentID: "0", Name: "f.bin", Size: d.size}}, nil
}
func (d *blockingReadDriver) Read(ctx context.Context, _ drive.Entry, offset, size int64) (io.ReadCloser, error) {
	select {
	case d.probe.arrived <- struct{}{}:
	default:
	}
	select {
	case <-d.probe.release:
		return io.NopCloser(bytes.NewReader(make([]byte, size))), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (d *blockingReadDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *blockingReadDriver) Capabilities() []drive.Capability { return nil }
func (d *blockingReadDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{}, nil
}
func (d *blockingReadDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

// TestReadAtIntoWithPriorityHighUsesReserveSlots proves the priority is
// honored by the real read scheduler through Core: it saturates the normal
// slot pool with parked reads, then shows a PriorityHigh read still proceeds
// (reserve slots) while another normal-priority read stays starved. Reads of
// distinct windows never coalesce, and every parked driver read holds its
// slot until the release gate closes, so the outcome is deterministic.
func TestReadAtIntoWithPriorityHighUsesReserveSlots(t *testing.T) {
	const fileSize = 128 * read.ChunkSize
	probe := &blockingReadProbe{
		arrived: make(chan struct{}, 256),
		release: make(chan struct{}),
	}
	raw := &blockingReadDriver{probe: probe, size: fileSize}
	fs, err := vfs.New(raw, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer stopTestVFS(t, fs)
	// releaseGate closes probe.release exactly once on every exit path —
	// including t.Fatal early exits — so parked driver reads and the starved
	// read always finish, and no goroutine leaks from this test. Registered
	// last so it runs first on teardown, before the cache close below.
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(probe.release) }) }
	defer releaseGate()
	fs.Start(ctx)
	c := newTestCore(t, fs)

	readBytes := func(ctx context.Context, offset int64) ([]byte, error) {
		buf := make([]byte, 16)
		n, err := c.ReadAtInto(ctx, "/f.bin", offset, buf, 0)
		return buf[:n], err
	}

	type outcome struct {
		data []byte
		err  error
	}
	normalSlots := read.MaxConcurrency - read.HighReserve
	normalResults := make([]outcome, normalSlots)
	var normalWG sync.WaitGroup
	for i := 0; i < normalSlots; i++ {
		normalWG.Add(1)
		go func(i int) {
			defer normalWG.Done()
			// Spread offsets across distinct windows so no two reads
			// coalesce onto one load. Every parked read uses the cancellable
			// test ctx so the probe driver parks it until releaseGate.
			data, err := readBytes(ctx, int64(i)*2*read.ChunkSize)
			normalResults[i] = outcome{data: data, err: err}
		}(i)
	}
	// Park until every normal slot is held by a parked driver read.
	for i := 0; i < normalSlots; i++ {
		select {
		case <-probe.arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("normal-priority reads did not saturate the slot pool")
		}
	}

	// A high-priority read must proceed through the reserve slot pool even
	// though every normal slot is held.
	highResult := make(chan outcome, 1)
	go func() {
		data, err := readBytesWithPriority(ctx, c, 64*read.ChunkSize, PriorityHigh)
		highResult <- outcome{data: data, err: err}
	}()
	select {
	case <-probe.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("PriorityHigh read did not reach the driver while the normal pool was saturated")
	}

	// A second normal-priority read launched now must stay starved: every
	// normal slot is still held, so it never reaches the driver before
	// release. Reading window 80 (vs 0..46 for the parked reads and 64 for
	// the high read) keeps all three read sets on distinct, non-coalescing
	// offsets.
	starvedResult := make(chan outcome, 1)
	go func() {
		data, err := readBytes(ctx, 80*read.ChunkSize)
		starvedResult <- outcome{data: data, err: err}
	}()
	select {
	case <-probe.arrived:
		t.Fatal("starved normal-priority read reached the driver while the normal pool was saturated")
	case <-time.After(400 * time.Millisecond):
	}

	// Release every parked read; all reads must now complete without error
	// and with the expected bytes.
	releaseGate()
	normalWG.Wait()
	for i, got := range normalResults {
		if got.err != nil {
			t.Fatalf("parked normal read %d error: %v", i, got.err)
		}
		if len(got.data) != 16 {
			t.Fatalf("parked normal read %d bytes = %d, want 16", i, len(got.data))
		}
	}
	high := <-highResult
	if high.err != nil {
		t.Fatalf("high-priority read error: %v", high.err)
	}
	if len(high.data) != 16 {
		t.Fatalf("high-priority read bytes = %d, want 16", len(high.data))
	}
	starved := <-starvedResult
	if starved.err != nil {
		t.Fatalf("starved normal read after release error: %v", starved.err)
	}
	if len(starved.data) != 16 {
		t.Fatalf("starved normal read bytes = %d, want 16", len(starved.data))
	}
}

func readBytesWithPriority(ctx context.Context, c *Core, offset int64, priority ReadPriority) ([]byte, error) {
	buf := make([]byte, 16)
	n, err := c.ReadAtIntoWithPriority(ctx, "/f.bin", offset, buf, 0, priority)
	return buf[:n], err
}
