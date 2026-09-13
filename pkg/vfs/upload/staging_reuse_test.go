package upload

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// frozenTestStore returns a store holding one frozen generation with stale
// source hashes, the state Flush leaves behind.
func frozenTestStore(t *testing.T) (*PendingStore, PendingUpload) {
	t.Helper()
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	localPath, err := store.CreateStaging("file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteStagingAt(localPath, []byte("hello"), 0); err != nil {
		t.Fatal(err)
	}
	pending := PendingUpload{
		Path:         "/file.txt",
		FID:          "file",
		ParentID:     "root",
		Name:         "file.txt",
		LocalPath:    localPath,
		Size:         5,
		Frozen:       true,
		SourceHashes: drive.SourceHashes{drive.HashSHA256: []byte("stale")},
	}
	if err := store.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}
	current, ok := store.UploadByPath(pending.Path)
	if !ok {
		t.Fatal("pending upload not recorded")
	}
	return store, current
}

// TestTryReuseFrozenStagingRevivesUnclaimedGeneration pins the write-path side
// of the claim protocol: a frozen generation no upload has started reading is
// reusable in place, and the hashes of the frozen bytes go with the freeze.
func TestTryReuseFrozenStagingRevivesUnclaimedGeneration(t *testing.T) {
	store, frozen := frozenTestStore(t)
	reused, ok := store.TryReuseFrozenStaging(frozen)
	if !ok {
		t.Fatal("unclaimed frozen generation was not reusable")
	}
	if reused.Frozen {
		t.Fatal("reused generation stayed frozen")
	}
	if reused.FID != frozen.FID || reused.LocalPath != frozen.LocalPath {
		t.Fatalf("reuse changed the generation identity: %+v -> %+v", frozen, reused)
	}
	if reused.SourceHashes != nil {
		t.Fatalf("reused generation kept hashes of the frozen bytes: %v", reused.SourceHashes)
	}
	if reused.UpdatedAt == frozen.UpdatedAt {
		t.Fatal("reuse did not retire queued attempts of the frozen generation")
	}
	current, ok := store.UploadByPath(frozen.Path)
	if !ok || current.Frozen || current.UploadStarted {
		t.Fatalf("stored record after reuse = %+v, want a mutable unclaimed generation", current)
	}
	if _, ok := store.TryReuseFrozenStaging(reused); ok {
		t.Fatal("mutable generation reported as reusable")
	}
}

// TestTryReuseFrozenStagingRejectsClaimedGeneration covers the mirror order:
// once an upload claimed the generation its staging file must stay immutable,
// so the next write has to rotate.
func TestTryReuseFrozenStagingRejectsClaimedGeneration(t *testing.T) {
	store, frozen := frozenTestStore(t)
	claimed, ok := store.MarkUploadStarted(frozen)
	if !ok {
		t.Fatal("fresh generation could not be claimed")
	}
	if !claimed.UploadStarted {
		t.Fatal("claim did not mark the generation")
	}
	if _, ok := store.TryReuseFrozenStaging(frozen); ok {
		t.Fatal("claimed generation was reused in place")
	}
}

// TestMarkUploadStartedRejectsReusedGeneration pins the other order: a
// generation a write already revived is no longer a frozen snapshot, so an
// upload must not start reading it.
func TestMarkUploadStartedRejectsReusedGeneration(t *testing.T) {
	store, frozen := frozenTestStore(t)
	if _, ok := store.TryReuseFrozenStaging(frozen); !ok {
		t.Fatal("unclaimed frozen generation was not reusable")
	}
	if _, ok := store.MarkUploadStarted(frozen); ok {
		t.Fatal("upload claimed a generation the write path already reused")
	}
}

// TestMarkUploadStartedKeepsGenerationIdentity pins that the claim is
// invisible to the record comparisons that decide supersession: a requeued or
// retried attempt of the same generation must still be recognized as the same
// generation, or its upload would be dropped as stale.
func TestMarkUploadStartedKeepsGenerationIdentity(t *testing.T) {
	store, frozen := frozenTestStore(t)
	claimed, ok := store.MarkUploadStarted(frozen)
	if !ok {
		t.Fatal("fresh generation could not be claimed")
	}
	if !SameUploadRecord(claimed, frozen) || !sameUploadRecord(claimed, frozen) {
		t.Fatal("claim changed the generation identity")
	}
	if _, ok := store.MarkUploadStarted(claimed); !ok {
		t.Fatal("already claimed generation could not be claimed again")
	}
	// A reused generation does change identity, which is what retires a queued
	// attempt of the frozen bytes.
	store2, frozen2 := frozenTestStore(t)
	reused, ok := store2.TryReuseFrozenStaging(frozen2)
	if !ok {
		t.Fatal("unclaimed frozen generation was not reusable")
	}
	if SameUploadRecord(reused, frozen2) || sameUploadRecord(reused, frozen2) {
		t.Fatal("reuse left the generation looking unchanged")
	}
}
