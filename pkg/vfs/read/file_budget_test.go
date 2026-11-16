package read

// File in-flight budget tests: the stream count a window recommends is
// independent of how many Range requests one file may have in flight, and the
// budget counts foreground and speculative ranges together.

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type asyncRead struct {
	data []byte
	err  error
}

// goRead performs one read on its own goroutine so a budget regression shows up
// as a timeout instead of a hung test binary.
func (s *stagedReader) goRead(ctx context.Context, offset, size int64) <-chan asyncRead {
	out := make(chan asyncRead, 1)
	go func() {
		rc, err := s.reader.Read(ctx, "/file.bin", offset, size)
		if err != nil {
			out <- asyncRead{err: err}
			return
		}
		data, readErr := readAllFrom(rc)
		out <- asyncRead{data: data, err: readErr}
	}()
	return out
}

func readAllFrom(rc io.ReadCloser) ([]byte, error) {
	defer rc.Close()
	return io.ReadAll(rc)
}

func budgetProfile(budget int) drive.ReadPolicy {
	profile := drive.DefaultReadPolicy()
	profile.Bulk.Streams = 1
	profile.MaxInFlight = budget
	return profile
}

// blockEveryRange gates every Range until release is closed, so concurrency is
// observed directly instead of inferred from timing.
func blockEveryRange(release <-chan struct{}) func(int64, int64) (<-chan struct{}, bool) {
	return func(int64, int64) (<-chan struct{}, bool) { return release, true }
}

func waitFilePermitsReleased(t *testing.T, r *stagedReader) {
	t.Helper()
	key := r.host.entry.ID
	deadline := time.Now().Add(2 * time.Second)
	for r.state.scheduler.PerFileInUse(key) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("file permits still held: %d", r.state.scheduler.PerFileInUse(key))
		}
		time.Sleep(time.Millisecond)
	}
}

func runConcurrentReads(t *testing.T, budget int) (maxActive int) {
	t.Helper()
	r := newStagedReaderWithProfile(t, 16<<20, budgetProfile(budget))
	release := make(chan struct{})
	var once sync.Once
	r.host.gateFn = blockEveryRange(release)

	first := r.goRead(sessionContext(1, 1), 4<<20, ChunkSize)
	second := r.goRead(sessionContext(2, 1), 8<<20, ChunkSize)
	// Give both reads time to reach the permit or the provider.
	time.Sleep(150 * time.Millisecond)
	maxActive = r.host.maxConcurrent()
	once.Do(func() { close(release) })
	for _, done := range []<-chan asyncRead{first, second} {
		select {
		case result := <-done:
			if result.err != nil {
				t.Fatalf("read: %v", result.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("read did not finish after the gate was released")
		}
	}
	waitFilePermitsReleased(t, r)
	return maxActive
}

func TestFileBudgetSerializesConcurrentForegroundReads(t *testing.T) {
	if got := runConcurrentReads(t, 1); got > 1 {
		t.Fatalf("concurrent ranges = %d, want at most the single permit", got)
	}
}

func TestFileBudgetPipelinesConcurrentForegroundReads(t *testing.T) {
	if got := runConcurrentReads(t, 2); got != 2 {
		t.Fatalf("concurrent ranges = %d, want the two-permit budget", got)
	}
}

func TestFileBudgetSeekCancelsThenAcquires(t *testing.T) {
	r := newStagedReaderWithProfile(t, 8<<20, budgetProfile(1))
	// The gate stays closed: only cancellation can release the lookahead.
	release := make(chan struct{})
	defer close(release)
	r.host.gateFn = func(offset, size int64) (<-chan struct{}, bool) {
		if offset == ChunkSize {
			return release, true
		}
		return nil, false
	}
	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	r.host.waitCall(t, 2)

	// A seek in the same session must drop its own obsolete lookahead before
	// it competes for the single permit.
	done := r.goRead(sessionContext(1, 2), 12*ChunkSize, ChunkSize)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("seek read: %v", result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("seek waited on its own obsolete prefetch permit")
	}
	waitFilePermitsReleased(t, r)
}

// A speculative window takes the same two permits as a foreground range, in
// the same order, and only when both are free: otherwise it could hold the
// file permit while a foreground read owns the only slot, and the wait would
// never end because the prefetch fetch (whose timeout releases it) never
// starts.
func TestPrefetchSkipsWithoutHoldingFilePermitWhenNoSlot(t *testing.T) {
	r := newStagedReaderWithProfile(t, 8<<20, budgetProfile(1))

	var releases []func()
	for range r.state.scheduler.Stats().NormalCapacity {
		release, err := acquireTestSlot(r.state.scheduler, context.Background(), PriorityNormal)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()

	r.reader.prefetchWindow(context.Background(), r.host.entry, 1, 1)

	if got := r.state.scheduler.PerFileInUse(r.host.entry.ID); got != 0 {
		t.Fatalf("prefetch holds %d file permits while it has no slot, want 0", got)
	}
	for _, call := range r.host.recorded() {
		if call.Prefetch {
			t.Fatal("prefetch issued a Range without a slot")
		}
	}
	if windows, prefetches, _ := r.state.RuntimeStats(); windows != 0 || prefetches != 0 {
		t.Fatalf("prefetch registered without a slot: windows=%d permits=%d", windows, prefetches)
	}
}

// A speculative window runs on the permits it took at admission: one slot and
// one file budget permit, no matter how many the budget would allow. A second
// pair inside the fetch would let a lookahead consume a foreground read's
// budget while it is still queued.
func TestPrefetchUsesAdmissionPermits(t *testing.T) {
	for _, budget := range []int{1, 2} {
		t.Run(fmt.Sprintf("budget_%d", budget), func(t *testing.T) {
			r := newStagedReaderWithProfile(t, 8<<20, budgetProfile(budget))
			release := make(chan struct{})
			r.host.gateFn = blockEveryRange(release)
			r.reader.prefetchWindow(context.Background(), r.host.entry, 1, 1)
			r.host.waitCall(t, 1)
			if got := r.state.scheduler.PerFileInUse(r.host.entry.ID); got != 1 {
				t.Fatalf("prefetch holds %d file permits, want exactly one", got)
			}
			if stats := r.state.scheduler.Stats(); stats.NormalInUse != 1 {
				t.Fatalf("prefetch holds %d global slots, want exactly one", stats.NormalInUse)
			}
			close(release)
			waitFilePermitsReleased(t, r)
		})
	}
}

func TestFileBudgetBoundsParallelSubRanges(t *testing.T) {
	profile := drive.DefaultReadPolicy()
	profile.Bulk.Streams = 4
	profile.Bulk.Split.MinParallelBytes = 0
	profile.MaxInFlight = 2
	r := newStagedReaderWithProfile(t, 16<<20, profile)
	ctx := WithoutReadPrefetch(WithReadIntent(sessionContext(1, 1), ReadIntentBulk))
	r.read(t, ctx, 0, 4<<20)
	// Four recommended streams with a two-permit budget must at most double up.
	if got := r.host.maxConcurrent(); got > 2 {
		t.Fatalf("concurrent ranges = %d, want at most the 2-permit budget", got)
	}
	waitFilePermitsReleased(t, r)
}
