package listing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type fakeRuntimeHost struct {
	stubHost
	children         []drive.Entry
	childErr         error
	current          bool
	committedPath    string
	committed        []drive.Entry
	committedExpires time.Time
	committedOK      bool
	entry            drive.Entry
	entryOK          bool
	// listBlock, when non-nil, blocks ListChildren until closed so tests
	// can hold the owner of a list load open while waiters arrive.
	listBlock    chan struct{}
	projectCalls int
}

func (h *fakeRuntimeHost) ListChildren(ctx context.Context, _ string) ([]drive.Entry, error) {
	if h.listBlock != nil {
		select {
		case <-h.listBlock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return h.children, h.childErr
}

func (h *fakeRuntimeHost) IsUnavailable(string) bool { return false }

func (h *fakeRuntimeHost) Entry(string) (drive.Entry, bool) {
	return h.entry, h.entryOK
}

func (h *fakeRuntimeHost) ProjectChildren(_ string, entries []drive.Entry) []drive.Entry {
	h.projectCalls++
	return entries
}

func (h *fakeRuntimeHost) CommitRemoteChildren(parentPath string, remote []drive.Entry, expires time.Time) []drive.Entry {
	h.committedPath = parentPath
	h.committed = remote
	h.committedExpires = expires
	h.committedOK = true
	return remote
}

func TestLoadRemoteChildrenWithRuntimeCommitsBackendEntries(t *testing.T) {
	host := &fakeRuntimeHost{
		children: []drive.Entry{{ID: "child", Name: "child.txt"}},
		current:  true,
		entry:    drive.Entry{ID: "parent", Name: "dir", IsDir: true},
	}
	lister := NewLister(ListerDeps{Remote: host, View: host, State: NewState()})
	entries, err := loadRemoteChildrenWithRuntime(context.Background(), "/dir", "parent", false, lister)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "child" {
		t.Fatalf("entries = %+v", entries)
	}
	if !host.committedOK || host.committedPath != "/dir" || len(host.committed) != 1 {
		t.Fatalf("commit path=%q entries=%+v ok=%v", host.committedPath, host.committed, host.committedOK)
	}
}

func TestLoadRemoteChildrenWithRuntimeDiscardsStalePrefetch(t *testing.T) {
	host := &fakeRuntimeHost{
		children: []drive.Entry{{ID: "child", Name: "child.txt"}},
		entry:    drive.Entry{ID: "other", Name: "dir", IsDir: true},
	}
	lister := NewLister(ListerDeps{Remote: host, View: host, State: NewState()})
	_, err := loadRemoteChildrenWithRuntime(context.Background(), "/dir", "parent", true, lister)
	if err == nil {
		t.Fatal("expected stale prefetch error")
	}
	if host.committedOK {
		t.Fatalf("stale prefetch committed entries: %+v", host.committed)
	}
}

func TestLoadRemoteChildrenWithRuntimeReturnsBackendError(t *testing.T) {
	wantErr := errors.New("list failed")
	host := &fakeRuntimeHost{childErr: wantErr}
	lister := NewLister(ListerDeps{Remote: host, View: host, State: NewState()})
	_, err := loadRemoteChildrenWithRuntime(context.Background(), "/dir", "parent", false, lister)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}
