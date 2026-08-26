package pathlock

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestStateReleasesEntriesAfterUnlock(t *testing.T) {
	s := New()
	for i := 0; i < 100; i++ {
		unlock := s.Lock("/some/path")
		unlock()
	}
	s.mu.Lock()
	n := len(s.locks)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("pathlock retains %d entries after unlock, want 0", n)
	}
}

func TestStateMutualExclusion(t *testing.T) {
	s := New()
	var counter atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				unlock := s.Lock("/hot/path")
				cur := counter.Load()
				counter.Store(cur + 1)
				unlock()
			}
		}()
	}
	wg.Wait()
	if got := counter.Load(); got != 50*20 {
		t.Fatalf("counter = %d, want %d (mutual exclusion lost)", got, 50*20)
	}
	s.mu.Lock()
	n := len(s.locks)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("pathlock retains %d entries after unlock, want 0", n)
	}
}
