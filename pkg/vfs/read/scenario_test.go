package read

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// scenarioHost adapts a FakeDriver to the read domain's Host surface so the
// scenario tests exercise the real Reader/State machinery without a VFS.
type scenarioHost struct {
	driver   *drive.FakeDriver
	entry    drive.Entry
	cacheKey string
}

func (h *scenarioHost) Resolve(context.Context, string) (drive.Entry, error) { return h.entry, nil }

func (*scenarioHost) PendingUpload(string) (vfstypes.PendingUpload, bool, error) {
	return vfstypes.PendingUpload{}, false, nil
}

func (h *scenarioHost) ReadCacheKey(drive.Entry) string { return h.cacheKey }

func (h *scenarioHost) DriverRead(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	return h.driver.Read(ctx, entry, offset, size)
}

func scenarioContent(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i)
	}
	return data
}

func newScenarioReader(t *testing.T, size int, behavior drive.ReadBehavior) (*Reader, *State, *drive.FakeDriver) {
	t.Helper()
	driver := drive.NewFakeDriver()
	driver.ReadBehavior = behavior
	if err := driver.Seed(map[string]string{"file.bin": string(scenarioContent(size))}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	entry := drive.Entry{
		ID:      "0/file.bin",
		Name:    "file.bin",
		Size:    int64(size),
		ModTime: time.Unix(1700000000, 0),
	}
	state := NewState(nil)
	profile := drive.DefaultReadPolicy()
	profile.Bulk.Split.MinParallelBytes = 0
	profile.Bulk.WindowBytes = 4 * ChunkSize
	reader := newTestReader(testReaderDeps{
		Host:    &scenarioHost{driver: driver, entry: entry, cacheKey: "cache"},
		State:   state,
		Profile: profile,
	})
	t.Cleanup(func() { _ = state.Close() })
	return reader, state, driver
}

func readScenario(t *testing.T, reader *Reader, ctx context.Context, offset, size int64) []byte {
	t.Helper()
	rc, err := reader.Read(ctx, "/file.bin", offset, size)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return data
}

func waitReadCalls(t *testing.T, driver *drive.FakeDriver, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for driver.ReadStats().Calls < want {
		if time.Now().After(deadline) {
			t.Fatalf("read calls = %d, want at least %d", driver.ReadStats().Calls, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestScenarioBulkReadUsesParallelRanges(t *testing.T) {
	reader, _, driver := newScenarioReader(t, 4<<20, drive.ReadBehavior{})
	driver.ReadDelay = 20 * time.Millisecond
	ctx := WithoutReadPrefetch(WithReadIntent(context.Background(), ReadIntentBulk))

	data := readScenario(t, reader, ctx, 0, 2<<20)
	if !bytes.Equal(data, scenarioContent(4 << 20)[:2<<20]) {
		t.Fatal("bulk read returned wrong bytes")
	}
	stats := driver.ReadStats()
	if stats.Calls < 2 {
		t.Fatalf("driver calls = %d, want at least 2", stats.Calls)
	}
	if stats.MaxConcurrent < 2 {
		t.Fatalf("max concurrent reads = %d, want at least 2", stats.MaxConcurrent)
	}
}

func TestScenarioPerFileParallelCap(t *testing.T) {
	reader, _, driver := newScenarioReader(t, 12<<20, drive.ReadBehavior{})
	driver.ReadDelay = 20 * time.Millisecond
	gate := make(chan struct{})
	driver.ReadBehavior.BodyGate = gate
	var gateOnce sync.Once
	releaseGate := func() { gateOnce.Do(func() { close(gate) }) }
	defer releaseGate()

	ctx, cancel := context.WithCancel(WithoutReadPrefetch(WithReadIntent(context.Background(), ReadIntentBulk)))
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		offset := int64(i) * (4 << 20)
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc, err := reader.Read(ctx, "/file.bin", offset, 2<<20)
			if err != nil {
				errs <- err
				return
			}
			defer rc.Close()
			_, err = io.Copy(io.Discard, rc)
			errs <- err
		}()
	}

	waitReadCalls(t, driver, 4)
	releaseGate()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}

	stats := driver.ReadStats()
	if stats.MaxConcurrent > 4 {
		t.Fatalf("max concurrent reads = %d, want at most 4", stats.MaxConcurrent)
	}
	if stats.MaxConcurrent < 2 {
		t.Fatalf("max concurrent reads = %d, want at least 2", stats.MaxConcurrent)
	}
}

func TestScenarioCancelReleasesReadSlot(t *testing.T) {
	host := &blockingHost{
		entry:    drive.Entry{ID: "id", Size: 4 * ChunkSize, ModTime: time.Now()},
		entered:  make(chan struct{}, 1),
		canceled: make(chan struct{}),
	}
	state := NewState(nil)
	t.Cleanup(func() { _ = state.Close() })
	reader := newTestReader(testReaderDeps{Host: host, State: state})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		rc, err := reader.Read(ctx, "/file", 0, ChunkSize)
		if rc != nil {
			rc.Close()
		}
		done <- err
	}()

	select {
	case <-host.entered:
	case <-time.After(time.Second):
		t.Fatal("read did not enter the driver")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("read error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("read did not return after cancel")
	}
	stats := state.scheduler.Stats()
	if stats.NormalInUse != 0 || stats.HighInUse != 0 {
		t.Fatalf("slots after cancel: normal=%d high=%d, want 0", stats.NormalInUse, stats.HighInUse)
	}
}

func TestScenarioFakeDriverPrefetchCancelsOnInvalidate(t *testing.T) {
	// A lookahead Range is speculative: invalidating the file must cancel it
	// without touching the read that already delivered data.
	r := newStagedReader(t, 8<<20)
	release := make(chan struct{})
	defer close(release)
	r.host.gateFn = func(offset, size int64) (<-chan struct{}, bool) {
		if offset == ChunkSize {
			return release, true
		}
		return nil, false
	}
	r.read(t, sessionContext(7, 1), 0, ChunkSize)
	r.host.waitCall(t, 2)

	r.state.InvalidateFile("cache")
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, call := range r.host.recorded() {
			if call.Offset == ChunkSize && call.Err != nil {
				if !errors.Is(call.Err, context.Canceled) {
					t.Fatalf("lookahead canceled with %v", call.Err)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("invalidate did not cancel the prefetch body")
		}
		time.Sleep(time.Millisecond)
	}
}
