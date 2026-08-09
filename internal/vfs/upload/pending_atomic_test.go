package upload

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// newPendingStoreFixture builds a store with a few pending uploads and
// staging files for write-ahead fault tests.
func newPendingStoreFixture(t *testing.T) *PendingStore {
	t.Helper()
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// addPending writes a pending record through the FORMAL API so the dirty
// journal is durable - replay-based tests depend on it.
func (c *PendingStore) addPending(t *testing.T, path, fid, local string) {
	t.Helper()
	if local != "" {
		if err := os.WriteFile(filepath.Join(c.staging.dir, local), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	err := c.SaveUploadExact(PendingUpload{
		Path: path, FID: fid, Name: path, LocalPath: filepath.Join(c.staging.dir, local),
		Size: 1, UpdatedAt: 1234567890,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (c *PendingStore) hasPending(path string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.pending[path]
	return ok
}

// TestRemoveUploadAppendFailureKeepsMemory: when the clean-journal append
// fails, the pending record, id index, and staging file all survive.
func TestRemoveUploadAppendFailureKeepsMemory(t *testing.T) {
	store := newPendingStoreFixture(t)
	store.addPending(t, "/a.txt", "fid-a", "a.staging")
	store.journalFail = func() error { return errors.New("append boom") }

	if err := store.RemoveUpload("/a.txt"); err == nil {
		t.Fatal("want append error")
	}
	if !store.hasPending("/a.txt") {
		t.Error("pending record lost after failed append")
	}
	if _, ok := store.idIndex["fid-a"]; !ok {
		t.Error("id index entry lost after failed append")
	}
	if _, err := os.Stat(filepath.Join(store.staging.dir, "a.staging")); err != nil {
		t.Error("staging file lost after failed append")
	}
}

// TestRemoveUploadsUnderBatchFailureKeepsAll: a clean_batch append failure
// leaves every pending record and staging file intact IN MEMORY AND AFTER
// REPLAY - the whole batch is written as one journal entry, so nothing is
// partially durable.
func TestRemoveUploadsUnderBatchFailureKeepsAll(t *testing.T) {
	store := newPendingStoreFixture(t)
	store.addPending(t, "/dir/a.txt", "fid-a", "a.staging")
	store.addPending(t, "/dir/b.txt", "fid-b", "b.staging")
	store.addPending(t, "/other.txt", "fid-c", "c.staging")

	store.journalFail = func() error { return errors.New("append boom") }
	if err := store.RemoveUploadsUnder("/dir"); err == nil {
		t.Fatal("want batch append error")
	}
	for _, p := range []string{"/dir/a.txt", "/dir/b.txt", "/other.txt"} {
		if !store.hasPending(p) {
			t.Errorf("pending %s lost on batch failure", p)
		}
	}
	if _, err := os.Stat(filepath.Join(store.staging.dir, "a.staging")); err != nil {
		t.Error("staging a lost on batch failure")
	}
	if _, err := os.Stat(filepath.Join(store.staging.dir, "b.staging")); err != nil {
		t.Error("staging b lost on batch failure")
	}
	// Replay must also keep everything: nothing was durable.
	reopened, err := NewPendingStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/dir/a.txt", "/dir/b.txt", "/other.txt"} {
		if !reopened.hasPending(p) {
			t.Errorf("replay lost %s after batch failure", p)
		}
	}
}

// TestRemoveUploadsUnderCleanBatchDurableAcrossReplay: a successful batch
// removal writes ONE clean_batch entry; a reopened store applies the whole
// batch (all records gone).
func TestRemoveUploadsUnderCleanBatchDurableAcrossReplay(t *testing.T) {
	store := newPendingStoreFixture(t)
	store.addPending(t, "/dir/a.txt", "fid-a", "a.staging")
	store.addPending(t, "/dir/b.txt", "fid-b", "b.staging")
	if err := store.RemoveUploadsUnder("/dir"); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewPendingStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/dir/a.txt", "/dir/b.txt"} {
		if reopened.hasPending(p) {
			t.Errorf("replay resurrected %s after clean_batch", p)
		}
	}
}

// TestRemoveUploadCompactFailureStaysDeleted: when the append succeeds but
// the compact fails, the delete is durable (clean intent persisted) - the
// method reports success and a reopened store still shows the record gone.
func TestRemoveUploadCompactFailureStaysDeleted(t *testing.T) {
	store := newPendingStoreFixture(t)
	store.addPending(t, "/a.txt", "fid-a", "a.staging")
	store.compactFail = func() error { return errors.New("compact boom") }

	if err := store.RemoveUpload("/a.txt"); err != nil {
		t.Fatalf("remove with compact failure should still succeed: %v", err)
	}
	if store.hasPending("/a.txt") {
		t.Fatal("pending still present after durable clean")
	}
	// Reopen from the same dir: the clean intent must keep it gone.
	reopened, err := NewPendingStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.hasPending("/a.txt") {
		t.Error("reopened store resurrected a cleaned record")
	}
}

// TestRemoveUploadSuccessConsistent: a successful remove leaves memory,
// index, journal, and staging consistent.
func TestRemoveUploadSuccessConsistent(t *testing.T) {
	store := newPendingStoreFixture(t)
	store.addPending(t, "/a.txt", "fid-a", "a.staging")
	if err := store.RemoveUpload("/a.txt"); err != nil {
		t.Fatal(err)
	}
	if store.hasPending("/a.txt") {
		t.Error("pending still present after remove")
	}
	if _, ok := store.idIndex["fid-a"]; ok {
		t.Error("id index entry still present after remove")
	}
	if _, err := os.Stat(filepath.Join(store.staging.dir, "a.staging")); !os.IsNotExist(err) {
		t.Error("staging file still present after remove")
	}
	// Reopen: the record stays gone.
	reopened, err := NewPendingStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.hasPending("/a.txt") {
		t.Error("reopened store resurrected the removed record")
	}
}

// TestConcurrentSaveRemoveSamePath: concurrent Save/Remove of the same path
// must be race-free and never leave a torn state (txMu serializes the
// snapshot->journal->memory transactions).
func TestConcurrentSaveRemoveSamePath(t *testing.T) {
	store := newPendingStoreFixture(t)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = store.SaveUploadExact(PendingUpload{
				Path: "/same.txt", FID: "fid-x", Name: "same.txt", Size: 1, UpdatedAt: 1234567890,
			})
		}
		close(done)
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = store.RemoveUpload("/same.txt")
		}
	}()
	wg.Wait()
}

// TestConcurrentRemoveUploadsUnderAndSave: a subtree removal racing a save
// under the same subtree is race-free.
func TestConcurrentRemoveUploadsUnderAndSave(t *testing.T) {
	store := newPendingStoreFixture(t)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = store.SaveUploadExact(PendingUpload{
				Path: "/dir/f.txt", FID: "fid-y", Name: "f.txt", Size: 1, UpdatedAt: 1234567890,
			})
		}
		close(done)
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = store.RemoveUploadsUnder("/dir")
		}
	}()
	wg.Wait()
}
