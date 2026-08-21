package upload

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type targetIndexRemote struct {
	mu          sync.Mutex
	listCalls   int
	entries     []drive.Entry
	listErr     error
	putErr      error
	removeErr   error
	renameErr   error
	removed     []string
	listStarted chan struct{}
	listRelease chan struct{}
	startOnce   sync.Once
}

func (r *targetIndexRemote) CanWrite() bool { return true }

func (r *targetIndexRemote) List(ctx context.Context, _ string) ([]drive.Entry, error) {
	r.mu.Lock()
	r.listCalls++
	entries := append([]drive.Entry(nil), r.entries...)
	err := r.listErr
	r.mu.Unlock()
	if r.listStarted != nil {
		r.startOnce.Do(func() { close(r.listStarted) })
	}
	if r.listRelease != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.listRelease:
		}
	}
	return entries, err
}

func (r *targetIndexRemote) PutSource(_ context.Context, req drive.UploadRequest) (drive.Entry, error) {
	if r.putErr != nil {
		return drive.Entry{}, r.putErr
	}
	size := int64(0)
	if req.Source != nil {
		size = req.Source.Size()
	}
	return drive.Entry{ID: "uploaded-" + req.Name, ParentID: req.ParentID, Name: req.Name, Size: size}, nil
}

func (r *targetIndexRemote) Remove(_ context.Context, entry drive.Entry) error {
	if r.removeErr != nil {
		return r.removeErr
	}
	r.mu.Lock()
	r.removed = append(r.removed, entry.ID)
	r.mu.Unlock()
	return nil
}

func (r *targetIndexRemote) Rename(_ context.Context, _ drive.Entry, _ string) error {
	return r.renameErr
}

func (r *targetIndexRemote) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listCalls
}

func TestTargetIndexCoalescesConcurrentParentLists(t *testing.T) {
	remote := &targetIndexRemote{listStarted: make(chan struct{}), listRelease: make(chan struct{})}
	index := NewTargetIndex(time.Minute)
	const workers = 32
	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, lease, _, err := index.prepare(context.Background(), remote, "parent", fmt.Sprintf("file-%d", n), fmt.Sprintf("fid-%d", n), "")
			if err == nil {
				index.release(lease)
			}
			errCh <- err
		}(n)
	}
	close(start)
	<-remote.listStarted
	close(remote.listRelease)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := remote.callCount(); got != 1 {
		t.Fatalf("List calls = %d, want 1", got)
	}
}

func TestTargetIndexSerializesSameFinalName(t *testing.T) {
	remote := &targetIndexRemote{}
	index := NewTargetIndex(time.Minute)
	_, first, _, err := index.prepare(context.Background(), remote, "parent", "same.txt", "fid-1", "")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan targetLease, 1)
	go func() {
		_, lease, _, prepareErr := index.prepare(context.Background(), remote, "parent", "same.txt", "fid-2", "")
		if prepareErr == nil {
			done <- lease
		}
	}()
	select {
	case lease := <-done:
		index.release(lease)
		t.Fatal("second preparation completed while the final name was reserved")
	case <-time.After(50 * time.Millisecond):
	}

	index.release(first)
	select {
	case lease := <-done:
		index.release(lease)
	case <-time.After(time.Second):
		t.Fatal("second preparation did not resume after reservation release")
	}
	if got := remote.callCount(); got != 1 {
		t.Fatalf("List calls = %d, want 1", got)
	}
}

func TestTargetIndexTracksSuccessfulUpload(t *testing.T) {
	remote := &targetIndexRemote{}
	index := NewTargetIndex(time.Minute)
	_, lease, _, err := index.prepare(context.Background(), remote, "parent", "file.txt", "fid-1", "")
	if err != nil {
		t.Fatal(err)
	}
	index.release(lease)

	indexed := indexedRemoteOps{RemoteOps: remote, index: index}
	entry, err := indexed.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "file.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("data")),
	})
	if err != nil {
		t.Fatal(err)
	}
	target, lease, _, err := index.prepare(context.Background(), indexed, "parent", "file.txt", "fid-2", "")
	if err != nil {
		t.Fatal(err)
	}
	index.release(lease)
	if target.UploadName != TemporaryUploadName("file.txt", "fid-2") {
		t.Fatalf("upload name = %q, want temporary replacement name", target.UploadName)
	}
	if len(target.ReplaceExisting) != 1 || target.ReplaceExisting[0].ID != entry.ID {
		t.Fatalf("replace entries = %+v, want uploaded entry %q", target.ReplaceExisting, entry.ID)
	}
	if got := remote.callCount(); got != 1 {
		t.Fatalf("List calls = %d, want 1", got)
	}
}

func TestTargetIndexExpiresAndRefreshes(t *testing.T) {
	remote := &targetIndexRemote{}
	index := NewTargetIndex(2 * time.Second)
	now := time.Unix(100, 0)
	index.now = func() time.Time { return now }

	for _, advance := range []time.Duration{0, time.Second, 2 * time.Second} {
		now = now.Add(advance)
		_, lease, _, err := index.prepare(context.Background(), remote, "parent", fmt.Sprintf("file-%d", now.Unix()), "fid", "")
		if err != nil {
			t.Fatal(err)
		}
		index.release(lease)
	}
	if got := remote.callCount(); got != 2 {
		t.Fatalf("List calls = %d, want 2 after TTL expiry", got)
	}
}

func TestTargetIndexInvalidatesAfterMutationFailure(t *testing.T) {
	remote := &targetIndexRemote{putErr: errors.New("upload failed")}
	index := NewTargetIndex(time.Minute)
	_, lease, _, err := index.prepare(context.Background(), remote, "parent", "first.txt", "fid-1", "")
	if err != nil {
		t.Fatal(err)
	}
	index.release(lease)

	indexed := indexedRemoteOps{RemoteOps: remote, index: index}
	if _, err := indexed.PutSource(context.Background(), drive.UploadRequest{ParentID: "parent", Name: "first.txt"}); err == nil {
		t.Fatal("PutSource succeeded, want failure")
	}
	_, lease, _, err = index.prepare(context.Background(), indexed, "parent", "second.txt", "fid-2", "")
	if err != nil {
		t.Fatal(err)
	}
	index.release(lease)
	if got := remote.callCount(); got != 2 {
		t.Fatalf("List calls = %d, want 2 after invalidation", got)
	}
}

func TestTargetIndexRemovesStaleTemporaryEntryOnce(t *testing.T) {
	stale := drive.Entry{ID: "stale", ParentID: "parent", Name: TemporaryUploadName("file.txt", "fid")}
	remote := &targetIndexRemote{entries: []drive.Entry{stale}}
	index := NewTargetIndex(time.Minute)
	indexed := indexedRemoteOps{RemoteOps: remote, index: index}

	for range 2 {
		_, lease, _, err := index.prepare(context.Background(), indexed, "parent", "file.txt", "fid", "")
		if err != nil {
			t.Fatal(err)
		}
		index.release(lease)
	}
	remote.mu.Lock()
	removed := append([]string(nil), remote.removed...)
	remote.mu.Unlock()
	if len(removed) != 1 || removed[0] != stale.ID {
		t.Fatalf("removed = %+v, want [%s]", removed, stale.ID)
	}
	if got := remote.callCount(); got != 1 {
		t.Fatalf("List calls = %d, want 1", got)
	}
}
