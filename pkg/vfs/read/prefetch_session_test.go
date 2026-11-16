package read

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func slidingProfile(budget int) drive.ReadPolicy {
	p := drive.DefaultReadPolicy()
	p.MaxInFlight = budget
	p.Interactive.Initial.Prefetch = drive.PrefetchPolicy{Mode: drive.PrefetchDisabled}
	p.Interactive.Sequential.Prefetch = drive.PrefetchPolicy{Mode: drive.PrefetchFixed, WindowBytes: 2 * ChunkSize, Depth: 3}
	return p
}

func sessionWorker(t *testing.T, r *stagedReader, session uint64) *prefetchSession {
	t.Helper()
	r.state.scopes.mu.Lock()
	scope := r.state.scopes.scopes[readScopeKey{cacheKey: "cache", sessionID: session}]
	r.state.scopes.mu.Unlock()
	if scope == nil {
		t.Fatal("missing session scope")
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.prefetch == nil {
		t.Fatal("missing session worker")
	}
	return scope.prefetch
}

func advanceSession(r *stagedReader, session, request uint64, cursor int64, plan ReadPlan) {
	advanceSessionConsumed(r, session, request, cursor, plan, deliveredRange{})
}

// advanceSessionConsumed is advanceSession plus the successful delivery that
// may grow a consumption-credit budget.
func advanceSessionConsumed(r *stagedReader, session, request uint64, cursor int64, plan ReadPlan, consumed deliveredRange) {
	ctx := sessionContext(session, request)
	r.state.observeReadAccess("cache", AccessHint{SessionID: session, RequestID: request}, (cursor-1)*ChunkSize, ChunkSize)
	r.reader.schedulePrefetch(ctx, r.host.entry, cursor-1, 1, true, plan, consumed)
}

func waitPrefetchIdle(t *testing.T, r *stagedReader) {
	t.Helper()
	waitUntil(t, "prefetch completion", func() bool {
		return r.state.scheduler.Stats().PrefetchInUse == 0
	})
}

func TestSessionPrefetchRefillsOnCompletionAndConsumption(t *testing.T) {
	r := newStagedReaderWithProfile(t, 16*ChunkSize, slidingProfile(1))
	gates := map[int64]chan struct{}{2: make(chan struct{}), 4: make(chan struct{}), 6: make(chan struct{}), 8: make(chan struct{})}
	r.host.gateFn = func(offset, _ int64) (<-chan struct{}, bool) {
		ch, ok := gates[offset/ChunkSize]
		return ch, ok
	}
	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	r.read(t, sessionContext(1, 2), ChunkSize, ChunkSize)
	r.host.waitCall(t, 3)
	for i, index := range []int64{2, 4, 6} {
		call := r.host.recorded()[2+i]
		if call.Offset != index*ChunkSize || call.Size != 2*ChunkSize || !call.Prefetch {
			t.Fatalf("out of order fill: %+v", call)
		}
		close(gates[index])
		if index != 6 {
			// No foreground read occurs here: completion alone fills the
			// next hole at budget one.
			r.host.waitCall(t, 4+i)
		}
	}
	waitPrefetchIdle(t, r)
	p := sessionWorker(t, r, 1)
	waitUntil(t, "completed horizon", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.completed) == 6
	})
	if r.host.callCount() != 5 {
		t.Fatalf("pause expanded beyond horizon: %+v", r.host.recorded())
	}
	func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, pending := range p.pending[:cap(p.pending)] {
			if pending.load != nil {
				t.Fatal("completed range buffer retained in pending capacity")
			}
		}
	}()
	got := r.read(t, sessionContext(1, 3), 2*ChunkSize, ChunkSize)
	if !bytes.Equal(got, r.host.data[2*ChunkSize:3*ChunkSize]) {
		t.Fatal("consumption changed bytes")
	}
	// A partial distant tail remains within the horizon but is batched to
	// avoid issuing a new tiny Range after every consumed cache block.
	r.read(t, sessionContext(1, 4), 3*ChunkSize, ChunkSize)
	r.host.waitCall(t, 6)
	call := r.host.recorded()[5]
	if call.Offset != 8*ChunkSize || call.Size != 2*ChunkSize {
		t.Fatalf("sliding tail exceeded new horizon: %+v", call)
	}
	close(gates[8])
	waitPrefetchIdle(t, r)
	if r.host.maxConcurrent() > 1 {
		t.Fatal("exceeded object budget")
	}
}

func TestSessionPrefetchWakesAfterExternalAdmissionRelease(t *testing.T) {
	for _, kind := range []string{"object", "prefetch", "global"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newStagedReaderWithProfile(t, 8*ChunkSize, slidingProfile(1))
				var releases []func()
				s := r.state.scheduler
				switch kind {
				case "object":
					release, err := s.AcquireRange(context.Background(), r.host.entry.ID, 1, PriorityNormal)
					if err != nil {
						t.Fatal(err)
					}
					releases = append(releases, release)
				case "prefetch":
					for s.TryAcquirePrefetch() {
						releases = append(releases, s.ReleasePrefetch)
					}
				case "global":
					for range s.Stats().NormalCapacity {
						release, err := s.AcquireRange(context.Background(), "other", 32, PriorityNormal)
						if err != nil {
							t.Fatal(err)
						}
						releases = append(releases, release)
					}
				}
				plan := ReadPlan{Stage: StageSequential, PrefetchChunks: 2, PrefetchDepth: 1}
				advanceSession(r, 1, 1, 1, plan)
				synctest.Wait()
				if r.host.callCount() != 0 {
					t.Fatal("prefetch bypassed occupied capacity")
				}
				for _, release := range releases {
					release()
				}
				r.host.waitCall(t, 1)
				waitPrefetchIdle(t, r)
				if r.host.callCount() != 1 {
					t.Fatalf("depth one multiplied requests: %+v", r.host.recorded())
				}
			})
		})
	}
}

func TestSessionPrefetchStopsOnSeekAndRetirement(t *testing.T) {
	for _, reason := range []string{"seek", "release", "close", "invalidate", "clear", "concurrent", "disabled"} {
		t.Run(reason, func(t *testing.T) {
			r := newStagedReaderWithProfile(t, 32*ChunkSize, slidingProfile(2))
			gate := make(chan struct{})
			r.host.gateFn = func(_, _ int64) (<-chan struct{}, bool) { return gate, true }
			plan := ReadPlan{Stage: StageSequential, PrefetchChunks: 2, PrefetchDepth: 1}
			advanceSession(r, 1, 1, 1, plan)
			advanceSession(r, 2, 1, 20, plan)
			r.host.waitCall(t, 2)
			switch reason {
			case "seek":
				// The new seek is a small foreground read; make only its
				// target available, leaving the other session blocked.
				r.state.putHotChunk("cache", 10, r.host.data[10*ChunkSize:11*ChunkSize])
				r.read(t, sessionContext(1, 2), 10*ChunkSize, 16<<10)
			case "release":
				r.state.ReleaseReadSession(1)
				// A delayed completion must not resurrect this session.
				r.reader.schedulePrefetch(sessionContext(1, 1), r.host.entry, 2, 1, true, plan, deliveredRange{})
			case "close":
				_ = r.state.Close()
				advanceSession(r, 1, 2, 10, plan)
			case "invalidate":
				r.state.InvalidateFile("cache")
			case "clear":
				_ = r.state.ClearReadCache()
			case "concurrent", "disabled":
				r.state.putHotChunk("cache", 1, r.host.data[ChunkSize:2*ChunkSize])
				ctx := WithAccessHint(context.Background(), AccessHint{SessionID: 1, RequestID: 2, Concurrent: reason == "concurrent"})
				if reason == "disabled" {
					ctx = WithoutReadPrefetch(ctx)
				}
				r.read(t, ctx, ChunkSize, 1)
			}
			want := 1
			if reason == "close" || reason == "invalidate" || reason == "clear" {
				want = 0
			}
			waitUntil(t, "retired session to drain", func() bool { return r.state.scheduler.Stats().PrefetchInUse == want })
			if r.host.callCount() != 2 {
				t.Fatalf("canceled session refilled: %+v", r.host.recorded())
			}
			close(gate)
		})
	}
}

type failingPrefetchHost struct{ *stagedHost }

func (h failingPrefetchHost) DriverRead(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	rc, err := h.stagedHost.DriverRead(ctx, entry, offset, size)
	if err != nil {
		return nil, err
	}
	_ = rc.Close()
	return nil, errors.New("permanent test failure")
}

func TestSessionPrefetchFailureAndEvictionDoNotRefetchOnWake(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "eviction", true: "failure"}[fail], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newStagedReader(t, 8*ChunkSize)
				if fail {
					h := failingPrefetchHost{r.host}
					r.reader = newTestReader(testReaderDeps{Host: h, State: r.state})
				}
				plan := ReadPlan{Stage: StageSequential, PrefetchChunks: 2, PrefetchDepth: 1}
				advanceSession(r, 1, 1, 1, plan)
				synctest.Wait()
				before := r.host.callCount()
				if before != 1 {
					t.Fatalf("initial request count %d", before)
				}
				for index := range HotChunkLimit + 1 {
					r.state.putHotChunk("other", int64(index), []byte{1})
				}
				for range 3 {
					r.state.scheduler.TryAcquirePrefetch()
					r.state.scheduler.ReleasePrefetch()
					synctest.Wait()
				}
				if r.host.callCount() != before {
					t.Fatal("completion/refill retried without consumption")
				}
				time.Sleep(prefetchSessionIdle + time.Nanosecond)
				synctest.Wait()
				r.state.scopes.mu.Lock()
				defer r.state.scopes.mu.Unlock()
				if len(r.state.scopes.scopes) != 0 {
					t.Fatal("idle worker retained scope")
				}
			})
		})
	}
}

func TestSessionPrefetchClampsEOFAndCountsExistingLookahead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newStagedReader(t, 6*ChunkSize+17)
		gate := make(chan struct{})
		r.host.gateFn = func(offset, _ int64) (<-chan struct{}, bool) {
			return gate, offset == 5*ChunkSize
		}
		// A warmup request already owns the session's only allowed window.
		r.reader.prefetchWindow(sessionContext(1, 1), r.host.entry, 5, 2)
		plan := ReadPlan{Stage: StageSequential, PrefetchChunks: 2, PrefetchDepth: 1}
		advanceSession(r, 1, 1, 4, plan)
		synctest.Wait()
		if r.host.callCount() != 1 {
			t.Fatal("controller ignored existing session window at depth one")
		}
		close(gate)
		synctest.Wait()
		calls := r.host.recorded()
		if len(calls) != 2 || calls[0].Size != ChunkSize+17 || calls[1].Offset != 4*ChunkSize || calls[1].Size != ChunkSize {
			t.Fatalf("EOF/coverage changed request bounds: %+v", calls)
		}
		advanceSession(r, 1, 2, 6, plan)
		synctest.Wait()
		if r.host.callCount() != 2 {
			t.Fatal("cached short tail was fetched again")
		}
	})
}

func TestSessionCancellationDoesNotWaitForPlanningMutex(t *testing.T) {
	r := newStagedReader(t, 8*ChunkSize)
	plan := ReadPlan{Stage: StageSequential, PrefetchChunks: 2, PrefetchDepth: 1}
	advanceSession(r, 1, 1, 1, plan)
	r.host.waitCall(t, 1)
	waitPrefetchIdle(t, r)
	p := sessionWorker(t, r, 1)
	updated := make(chan struct{})
	func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		go func() { advanceSession(r, 1, 2, 2, plan); close(updated) }()
		waitUntil(t, "consumption update waiting for planner", func() bool {
			if r.state.sequence.mu.TryLock() {
				r.state.sequence.mu.Unlock()
				return false
			}
			return true
		})
		canceled := make(chan struct{})
		go func() { r.state.cancelScopesBySession(1, "seek"); close(canceled) }()
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("scope cancellation acquired planner locks in reverse order")
		}
	}()
	select {
	case <-updated:
	case <-time.After(time.Second):
		t.Fatal("canceled update did not finish")
	}
}
