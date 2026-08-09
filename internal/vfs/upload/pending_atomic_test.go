package upload

import (
	"errors"
	"os"
	"path/filepath"
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

func (c *PendingStore) addPending(t *testing.T, path, fid, local string) {
	t.Helper()
	if local != "" {
		if err := os.WriteFile(filepath.Join(c.staging.dir, local), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c.mu.Lock()
	c.pending[path] = PendingUpload{Path: path, FID: fid, LocalPath: filepath.Join(c.staging.dir, local)}
	if fid != "" {
		c.idIndex[fid] = path
	}
	c.mu.Unlock()
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

// TestRemoveUploadsUnderBatchFailureKeepsAll: a mid-batch journal failure
// leaves every pending record and staging file intact.
func TestRemoveUploadsUnderBatchFailureKeepsAll(t *testing.T) {
	store := newPendingStoreFixture(t)
	store.addPending(t, "/dir/a.txt", "fid-a", "a.staging")
	store.addPending(t, "/dir/b.txt", "fid-b", "b.staging")
	store.addPending(t, "/other.txt", "fid-c", "c.staging")

	calls := 0
	store.journalFail = func() error {
		calls++
		if calls == 2 {
			return errors.New("append boom on second clean")
		}
		return nil
	}
	if err := store.RemoveUploadsUnder("/dir"); err == nil {
		t.Fatal("want batch append error")
	}
	if !store.hasPending("/dir/a.txt") {
		t.Error("first pending lost on mid-batch failure")
	}
	if !store.hasPending("/dir/b.txt") {
		t.Error("second pending lost on mid-batch failure")
	}
	if _, err := os.Stat(filepath.Join(store.staging.dir, "a.staging")); err != nil {
		t.Error("staging a lost on mid-batch failure")
	}
	if _, err := os.Stat(filepath.Join(store.staging.dir, "b.staging")); err != nil {
		t.Error("staging b lost on mid-batch failure")
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
