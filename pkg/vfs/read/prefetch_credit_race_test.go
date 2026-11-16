package read

// Concurrency bounds for the consumption-credit controller: a foreground
// consumer racing a seek, a session release, a cache clear and a close must
// still deliver correct bytes, must not resurrect a retired pass, and must
// release every shared permit.

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// consumeSequentially reads one chunk at a time from session 1, checking the
// bytes. It reports the first failure instead of calling t.Fatal from a
// goroutine.
func consumeSequentially(r *stagedReader, until int64, stop <-chan struct{}) error {
	for chunk := int64(0); chunk < until; chunk++ {
		select {
		case <-stop:
			return nil
		default:
		}
		rc, err := r.reader.Read(sessionContext(1, uint64(chunk+1)), "/file.bin", chunk*ChunkSize, ChunkSize)
		if err != nil {
			return fmt.Errorf("chunk %d: %w", chunk, err)
		}
		data, readErr := io.ReadAll(rc)
		_ = rc.Close()
		if readErr != nil {
			return fmt.Errorf("chunk %d: %w", chunk, readErr)
		}
		if !bytes.Equal(data, r.host.data[chunk*ChunkSize:(chunk+1)*ChunkSize]) {
			return fmt.Errorf("chunk %d returned wrong bytes", chunk)
		}
	}
	return nil
}

func TestSessionBudgetRaceWithRetirement(t *testing.T) {
	for _, action := range []string{"seek", "release", "cache_clear", "scope_close"} {
		t.Run(action, func(t *testing.T) {
			r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
			stop := make(chan struct{})
			var wg sync.WaitGroup
			errs := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := consumeSequentially(r, 24, stop); err != nil {
					select {
					case errs <- err:
					default:
					}
				}
			}()
			// Let the pass grow, then retire it from another goroutine while the
			// consumer keeps reading.
			time.Sleep(2 * time.Millisecond)
			switch action {
			case "seek":
				r.state.cancelScopesBySession(1, "seek")
			case "release":
				r.state.ReleaseReadSession(1)
			case "cache_clear":
				if err := r.state.ClearReadCache(); err != nil {
					t.Fatalf("clear read cache: %v", err)
				}
			case "scope_close":
				r.state.cancelAllScopes("close")
			}
			wg.Wait()
			close(stop)
			select {
			case err := <-errs:
				t.Fatalf("%s: %v", action, err)
			default:
			}
			// Resources stay bounded: every speculative permit comes back and no
			// controller outlives the pass.
			waitUntil(t, "prefetch permits released", func() bool {
				return r.state.scheduler.Stats().PrefetchInUse == 0
			})
			if n := sessionCount(r); n > 1 {
				t.Fatalf("%s left %d controllers", action, n)
			}
		})
	}
}

func TestSessionBudgetRaceWithConcurrentConsumers(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
	// Two sessions read different regions at once; each keeps its own budget
	// and neither may throttle the other's correctness.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, session := range []uint64{1, 2} {
		base := (session - 1) * 16
		wg.Add(1)
		go func() {
			defer wg.Done()
			for chunk := int64(0); chunk < 12; chunk++ {
				offset := (int64(base) + chunk) * ChunkSize
				rc, err := r.reader.Read(sessionContext(session, uint64(chunk+1)), "/file.bin", offset, ChunkSize)
				if err != nil {
					errs <- err
					return
				}
				data, readErr := io.ReadAll(rc)
				_ = rc.Close()
				if readErr != nil {
					errs <- readErr
					return
				}
				if !bytes.Equal(data, r.host.data[offset:offset+ChunkSize]) {
					errs <- fmt.Errorf("session %d chunk %d wrong bytes", session, chunk)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent consumer: %v", err)
	}
	waitUntil(t, "prefetch permits released", func() bool {
		return r.state.scheduler.Stats().PrefetchInUse == 0
	})
}

func TestSessionBudgetRaceWorkerAndClose(t *testing.T) {
	// Closing the read state while the controller is still filling must not
	// revive the worker or leak a permit; the pass is retired with the state.
	for range 20 {
		r := newStagedReaderWithProfile(t, 8<<20, drive.DefaultReadPolicy())
		r.read(t, sessionContext(1, 1), 0, ChunkSize)
		_ = r.state.Close()
		r.state.cancelAllScopes("close")
	}
}

func TestAdaptiveSessionKeepsTargetProgressUnderBudgetOne(t *testing.T) {
	// The adaptive adapter must not break the slow-link fix: with a single
	// shared permit the foreground still returns the target block while its
	// speculative tail is in flight.
	synctest.Test(t, func(t *testing.T) {
		profile := drive.DefaultReadPolicy()
		profile.MaxInFlight = 1
		r := newStagedReaderWithProfile(t, 8*ChunkSize, profile)
		h := slowPrefetchHost{stagedHost: r.host}
		source, err := drive.NewReadSource(hostReadDriver{host: h, policy: profile}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		r.reader = NewReader(ReaderDeps{Host: h, State: r.state, Source: source})
		// Grow a small adaptive budget, then read the block its lookahead owns.
		r.read(t, sessionContext(1, 1), 0, ChunkSize)
		synctest.Wait()
		p := sessionWorker(t, r, 1)
		if horizon, _, _ := sessionBudget(p); horizon == 0 {
			t.Fatal("no adaptive budget")
		}
		got := r.read(t, sessionContext(1, 2), ChunkSize, ChunkSize)
		if !bytes.Equal(got, r.host.data[ChunkSize:2*ChunkSize]) {
			t.Fatal("target block returned wrong bytes")
		}
		if r.host.maxConcurrent() > 1 {
			t.Fatalf("budget one exceeded: %d", r.host.maxConcurrent())
		}
	})
}
