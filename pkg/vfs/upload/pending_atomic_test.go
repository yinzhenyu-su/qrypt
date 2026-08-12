package upload

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

// ---- write-ahead fault/replay consistency (journal commit -> memory apply) ----

// TestSaveDirtyAppendFailureLeavesNoRecord: a failed dirty append must not
// touch memory, and a reopened store must see nothing either.
func TestSaveDirtyAppendFailureLeavesNoRecord(t *testing.T) {
	store := newPendingStoreFixture(t)
	store.journalFail = func() error { return errors.New("append boom") }
	err := store.SaveUploadExact(PendingUpload{Path: "/a.txt", FID: "fid-a", Name: "a.txt", Size: 1, UpdatedAt: 1234567890})
	if err == nil {
		t.Fatal("want append error")
	}
	if store.hasPending("/a.txt") {
		t.Error("memory got the record despite append failure")
	}
	reopened, err := NewPendingStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.hasPending("/a.txt") {
		t.Error("replay resurrected a record whose append failed")
	}
}

// TestRecordFailureAppendFailureKeepsOldState: a failed failure-state append
// must leave the old pending record (and its old retry count) intact.
func TestRecordFailureAppendFailureKeepsOldState(t *testing.T) {
	store := newPendingStoreFixture(t)
	store.addPending(t, "/a.txt", "fid-a", "a.staging")
	store.journalFail = func() error { return errors.New("append boom") }
	pending, ok, err := store.RecordUploadFailure("/a.txt", errors.New("boom"), time.Second)
	if err == nil {
		t.Fatal("want append error")
	}
	if !ok {
		t.Fatal("pending record should still exist")
	}
	if pending.RetryCount != 0 || pending.LastError != "" {
		t.Errorf("old state clobbered: retry=%d err=%q", pending.RetryCount, pending.LastError)
	}
	current, ok := store.UploadByPath("/a.txt")
	if !ok || current.RetryCount != 0 || current.LastError != "" {
		t.Errorf("memory state clobbered: %+v", current)
	}
	reopened, err := NewPendingStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if current, ok := reopened.UploadByPath("/a.txt"); !ok || current.RetryCount != 0 {
		t.Errorf("replay clobbered state: %+v", current)
	}
}

// TestRecordFailureIfUnchangedAppendFailureKeepsOldState: same invariant for
// the IfUnchanged variant.
func TestRecordFailureIfUnchangedAppendFailureKeepsOldState(t *testing.T) {
	store := newPendingStoreFixture(t)
	store.addPending(t, "/a.txt", "fid-a", "a.staging")
	pending, _ := store.UploadByPath("/a.txt")
	store.journalFail = func() error { return errors.New("append boom") }
	_, ok, err := store.RecordUploadFailureIfUnchanged(pending, errors.New("boom"), time.Second)
	if err == nil {
		t.Fatal("want append error")
	}
	if !ok {
		t.Fatal("pending record should still exist")
	}
	current, ok := store.UploadByPath("/a.txt")
	if !ok || current.RetryCount != 0 || current.LastError != "" {
		t.Errorf("memory state clobbered: %+v", current)
	}
	reopened, err := NewPendingStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if current, ok := reopened.UploadByPath("/a.txt"); !ok || current.RetryCount != 0 {
		t.Errorf("replay clobbered state: %+v", current)
	}
}

// TestRenameAppendFailureKeepsOld: a failed rename journal append must leave
// the old path present and the new path absent, in memory and on replay.
func TestRenameAppendFailureKeepsOld(t *testing.T) {
	store := newPendingStoreFixture(t)
	store.addPending(t, "/old.txt", "fid-old", "old.staging")
	store.journalFail = func() error { return errors.New("append boom") }
	err := store.RenameUpload("/old.txt", PendingUpload{Path: "/new.txt", FID: "fid-new", Name: "new.txt", Size: 1, UpdatedAt: 1234567890})
	if err == nil {
		t.Fatal("want append error")
	}
	if !store.hasPending("/old.txt") {
		t.Error("old path lost on rename append failure")
	}
	if store.hasPending("/new.txt") {
		t.Error("new path appeared on rename append failure")
	}
	reopened, err := NewPendingStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.hasPending("/old.txt") {
		t.Error("replay lost old path after rename append failure")
	}
	if reopened.hasPending("/new.txt") {
		t.Error("replay showed new path after rename append failure")
	}
}

// TestRenameDurableAcrossReplay: a successful rename must be identical in the
// current store and a reopened store.
func TestRenameDurableAcrossReplay(t *testing.T) {
	store := newPendingStoreFixture(t)
	store.addPending(t, "/old.txt", "fid-old", "old.staging")
	next := PendingUpload{Path: "/new.txt", FID: "fid-new", Name: "new.txt", Size: 1, UpdatedAt: 1234567890}
	if err := store.RenameUpload("/old.txt", next); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewPendingStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reopened.UploadByPath("/new.txt"); !ok || got.FID != "fid-new" {
		t.Errorf("replay rename result: %+v ok=%v", got, ok)
	}
	if reopened.hasPending("/old.txt") {
		t.Error("replay resurrected old path after rename")
	}
	// journal must contain exactly one rename entry (no clean+dirty pair)
	data, err := os.ReadFile(reopened.journalPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "\"rename\""); got != 1 {
		t.Errorf("rename journal entries = %d, want 1", got)
	}
}

// TestConcurrentSaveRemoveMatchesReplay: after concurrent Save/Remove races
// settle, the live PendingUploads must equal the reopened store's set - not
// just be race-free.
func TestConcurrentSaveRemoveMatchesReplay(t *testing.T) {
	store := newPendingStoreFixture(t)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
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
	reopened, err := NewPendingStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	live := store.PendingUploads()
	afterReplay := reopened.PendingUploads()
	if len(live) != len(afterReplay) {
		t.Fatalf("live %d pending vs replay %d", len(live), len(afterReplay))
	}
	for _, p := range live {
		if r, ok := reopened.UploadByPath(p.Path); !ok || !sameUploadRecord(p, r) {
			t.Errorf("path %s: live %+v vs replay %+v", p.Path, p, r)
		}
	}
}
