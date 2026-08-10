package listing

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestCommitRemoteChildrenSingleCall: List's remote-commit path must
// exercise exactly one semantic View operation (CommitRemoteChildren).
// The test double records the call; UpdateOverlay/CommitList are no longer
// part of View, so the lister provably cannot orchestrate the overlay and
// cache steps itself.
func TestCommitRemoteChildrenSingleCall(t *testing.T) {
	host := &fakeRuntimeHost{children: []drive.Entry{
		{ID: "c1", Name: "a.txt"},
		{ID: "c2", Name: "dir1", IsDir: true},
	}}
	l := NewLister(ListerDeps{Remote: host, View: host, State: NewState()})
	entries, err := l.List(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if !host.committedOK {
		t.Fatal("CommitRemoteChildren was not called on the remote-commit path")
	}
	if host.committedPath != "/" {
		t.Fatalf("committed path = %q, want /", host.committedPath)
	}
	if len(host.committed) != 2 {
		t.Fatalf("committed entries = %d, want 2", len(host.committed))
	}
	if len(entries) != 2 {
		t.Fatalf("listed entries = %d, want 2", len(entries))
	}
}

// TestCommitRemoteChildrenExpiryPassed: the fresh-list expiry derived from
// the remote fetch is handed to the view commit (list cache TTL).
func TestCommitRemoteChildrenExpiryPassed(t *testing.T) {
	host := &fakeRuntimeHost{children: []drive.Entry{{ID: "c1", Name: "a.txt"}}}
	l := NewLister(ListerDeps{Remote: host, View: host, State: NewState()})
	if _, err := l.List(context.Background(), "/"); err != nil {
		t.Fatal(err)
	}
	if !host.committedExpires.After(time.Now()) {
		t.Fatalf("committed expiry %v is not in the future", host.committedExpires)
	}
}

// TestProjectChildrenUsedByWaiter: an in-flight waiter projects the owner's
// fetched snapshot through ProjectChildren - the lister no longer calls the
// individual projection steps.
func TestProjectChildrenUsedByWaiter(t *testing.T) {
	host := &fakeRuntimeHost{
		children:  []drive.Entry{{ID: "c1", Name: "a.txt"}},
		listBlock: make(chan struct{}),
	}
	l := NewLister(ListerDeps{Remote: host, View: host, State: NewState()})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := l.List(context.Background(), "/"); err != nil {
			t.Errorf("owner list: %v", err)
		}
	}()
	// Give the owner time to become the load owner (blocked on the remote
	// fetch), then add a waiter.
	time.Sleep(20 * time.Millisecond)
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		if _, err := l.List(context.Background(), "/"); err != nil {
			t.Errorf("waiter list: %v", err)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	close(host.listBlock)
	<-done
	<-waiterDone
	if host.projectCalls == 0 {
		t.Fatal("waiter path did not call ProjectChildren")
	}
}

// TestChildrenIgnoresEntryCacheMiss: Children callers already hold the
// parentID from resolve; the entry cache may not have the parent yet. A
// cache miss must NOT be treated as unavailable/NotFound - only the
// visibility check gates the error. This guards the semantic split between
// View.IsUnavailable (visibility) and View.Entry (cache identity).
func TestChildrenIgnoresEntryCacheMiss(t *testing.T) {
	host := &fakeRuntimeHost{
		children: []drive.Entry{{ID: "c1", Name: "a.txt"}},
		// entry deliberately NOT set: Entry("/uncached") is a miss.
		// IsUnavailable stays false (path is visible).
	}
	l := NewLister(ListerDeps{Remote: host, View: host, State: NewState()})
	entries, err := l.Children(context.Background(), "/uncached", "known-parent-id")
	if err != nil {
		t.Fatalf("Children with entry-cache miss returned error: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("Children = %+v, want [a.txt]", entries)
	}
}
