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
