package listing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestStateCoalescesListLoads(t *testing.T) {
	scheduler := NewState()

	load, owner := scheduler.BeginListLoad("/dir", false)
	if !owner {
		t.Fatal("first load should own")
	}
	waiter, owner := scheduler.BeginListLoad("/dir", true)
	if owner {
		t.Fatal("second load should wait")
	}
	if waiter != load {
		t.Fatal("waiter did not receive existing load")
	}

	wantErr := errors.New("list failed")
	scheduler.FinishListLoad("/dir", load, []drive.Entry{{ID: "child", Name: "child.txt"}}, wantErr)
	<-waiter.done
	if !errors.Is(waiter.err, wantErr) {
		t.Fatalf("waiter err = %v, want %v", waiter.err, wantErr)
	}

	next, owner := scheduler.BeginListLoad("/dir", false)
	if !owner || next == load {
		t.Fatalf("next load owner=%v next==load=%v", owner, next == load)
	}
	scheduler.FinishListLoad("/dir", next, nil, nil)
}

func TestStateTracksDirPrefetchState(t *testing.T) {
	scheduler := NewState()

	if !scheduler.MarkDirPrefetch("/dir", false) {
		t.Fatal("first mark should schedule prefetch")
	}
	if scheduler.MarkDirPrefetch("/dir", false) {
		t.Fatal("in-flight mark should be rejected")
	}
	scheduler.FinishDirPrefetch("/dir")
	scheduler.MarkDirPrefetchComplete("/dir")
	if scheduler.MarkDirPrefetch("/dir", false) {
		t.Fatal("recently completed prefetch should be suppressed")
	}
}

func TestStateUsesStartedPrefetchContext(t *testing.T) {
	scheduler := NewState()
	fallback := context.Background()
	started, cancel := context.WithCancel(context.Background())

	if !scheduler.StartDirPrefetch(started) {
		t.Fatal("first prefetch start should win")
	}
	if scheduler.StartDirPrefetch(context.Background()) {
		t.Fatal("second prefetch start should be ignored")
	}
	if got := scheduler.DirPrefetchContext(fallback); got != started {
		t.Fatal("expected started prefetch context")
	}
	cancel()
	if got := scheduler.DirPrefetchContext(fallback); got != fallback {
		t.Fatal("expected fallback after started context is canceled")
	}
}

func TestListerDetectsFreshListCache(t *testing.T) {
	host := stubHost{cache: map[string]listCacheEntry{
		"/dir": {expires: time.Now().Add(time.Minute)},
		"/old": {expires: time.Now().Add(-time.Minute)},
	}}
	lister := NewLister(ListerDeps{Remote: host, View: host, State: NewState()})

	if !lister.HasFreshListCache("/dir") {
		t.Fatal("expected fresh cache")
	}
	if lister.HasFreshListCache("/old") {
		t.Fatal("expected stale cache")
	}
}
