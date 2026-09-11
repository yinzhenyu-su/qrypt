package upload

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// --- minimal fakes for the engine deps ---

type fakeCommitRemote struct{}

func (fakeCommitRemote) List(context.Context, string) ([]drive.Entry, error) { return nil, nil }
func (fakeCommitRemote) PutSource(context.Context, drive.UploadRequest) (drive.Entry, error) {
	return drive.Entry{}, nil
}
func (fakeCommitRemote) Remove(context.Context, drive.Entry) error         { return nil }
func (fakeCommitRemote) Rename(context.Context, drive.Entry, string) error { return nil }
func (fakeCommitRemote) CanWrite() bool                                    { return true }

type fakeCommitObserver struct{}

func (fakeCommitObserver) Start(PendingUpload)                                    {}
func (fakeCommitObserver) Event(string, string, time.Time, int64, map[string]any) {}
func (fakeCommitObserver) Extra(string, string, any)                              {}
func (fakeCommitObserver) State(string, string)                                   {}
func (fakeCommitObserver) Uploaded(string, int)                                   {}
func (fakeCommitObserver) Finish(string, string, string)                          {}
func (fakeCommitObserver) Metadata(string, string, []string)                      {}
func (fakeCommitObserver) HealthResult(string, error)                             {}

type fakeCommitSnapshot struct{}

func (fakeCommitSnapshot) SnapshotPending(PendingUpload) (Snapshot, error) {
	return Snapshot{}, nil
}

type fakeCommitFaults struct{}

func (fakeCommitFaults) ApplyCancelFault(ctx context.Context, p PendingUpload, progress drive.UploadProgress, obs Observer) (context.Context, drive.UploadProgress, func()) {
	return ctx, progress, func() {}
}

// recordingView records UploadView commit calls.
type recordingView struct {
	commits []string
	drops   []string
	staging string
}

func (v *recordingView) CommitUploadedEntry(path string, entry drive.Entry, stagingPath string) {
	v.commits = append(v.commits, path)
	v.staging = stagingPath
}

func (v *recordingView) DropUploadedEntry(path string, _ drive.Entry) {
	v.drops = append(v.drops, path)
}

type recordingInvalidations struct {
	paths           []string
	pendingAtNotify bool
	store           *PendingStore
}

func (r *recordingInvalidations) InvalidatePath(path string) {
	r.paths = append(r.paths, path)
	_, r.pendingAtNotify = r.store.UploadByPath(path)
}

// fakeCommitRuntime implements the upload Runtime surface (minus the view
// commit, which now lives on UploadView).
type fakeCommitRuntime struct{}

func (r *fakeCommitRuntime) ClearUploadHashes(string)      {}
func (r *fakeCommitRuntime) ModTimeFor(string) time.Time   { return time.Time{} }
func (r *fakeCommitRuntime) RetryDelay(int) time.Duration  { return 0 }
func (r *fakeCommitRuntime) Requeue(PendingUpload)         {}
func (r *fakeCommitRuntime) RequeueIfFrozen(PendingUpload) {}

// TestFinalizeUploadCommitsViaView: finalizeUpload folds the uploaded
// entry into the view through the injected UploadView with the staging
// path, and cleans the pending record.
func TestFinalizeUploadCommitsViaView(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPendingStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(dir, "staging", "up.staging")
	if err := os.WriteFile(staging, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending := PendingUpload{
		Path: "/up.txt", FID: "fid-up", Name: "up.txt", Size: 4, LocalPath: staging, Frozen: true,
		UpdatedAt: 1234567890,
	}
	if err := store.SaveUploadExact(pending); err != nil {
		t.Fatal(err)
	}

	view := &recordingView{}
	invalidations := &recordingInvalidations{store: store}
	e := NewEngine(EngineDeps{
		Remote:        &fakeCommitRemote{},
		Observer:      &fakeCommitObserver{},
		Pending:       NewStoreAdapter(store),
		Runtime:       &fakeCommitRuntime{},
		View:          view,
		Invalidations: invalidations,
		Snapshot:      &fakeCommitSnapshot{},
		Faults:        &fakeCommitFaults{},
	})

	state, text, err := e.finalizeUpload(context.Background(), pending, drive.Entry{ID: "remote-up", Name: "up.txt", Size: 4}, Snapshot{Path: staging}, time.Now())
	if err != nil {
		t.Fatalf("finalize: %v (%s)", err, text)
	}
	if state != SnapshotStateCompleted {
		t.Fatalf("state = %s, want completed", state)
	}
	if len(view.commits) != 1 || view.commits[0] != "/up.txt" {
		t.Fatalf("view commits = %v, want exactly one /up.txt", view.commits)
	}
	if view.staging != staging {
		t.Fatalf("view staging path = %q, want %q", view.staging, staging)
	}
	// The pending record is gone.
	if _, ok := store.UploadByPath("/up.txt"); ok {
		t.Fatal("pending record still present after finalize")
	}
	if len(invalidations.paths) != 1 || invalidations.paths[0] != "/up.txt" {
		t.Fatalf("invalidations = %v, want exactly one /up.txt", invalidations.paths)
	}
	if invalidations.pendingAtNotify {
		t.Fatal("invalidation was published before the pending record was removed")
	}
}

// recordingCommitRemote records the rolled-back uploads.
type recordingCommitRemote struct {
	fakeCommitRemote
	removed []string
}

func (r *recordingCommitRemote) Remove(_ context.Context, entry drive.Entry) error {
	r.removed = append(r.removed, entry.ID)
	return nil
}

// TestFinalizeUploadSupersededDropsCommittedEntry: a generation that moved on
// while its upload was running must not stay in the view. The commit cannot be
// made conditional on the record claim without letting readers observe a path
// with neither a pending record nor a committed entry, so the lost claim takes
// the committed entry back out and rolls the uploaded replacement back; the
// newer generation stays untouched.
func TestFinalizeUploadSupersededDropsCommittedEntry(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPendingStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(dir, "staging", "up.staging")
	if err := os.WriteFile(staging, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending := PendingUpload{
		Path: "/up.txt", FID: "fid-one", Name: "up.txt", Size: 4, LocalPath: staging, Frozen: true,
		UpdatedAt: 1234567890,
	}
	if err := store.SaveUploadExact(pending); err != nil {
		t.Fatal(err)
	}
	// The file changed while the upload was in flight: a newer generation owns
	// the path by the time the upload finishes.
	next := pending
	next.FID = "fid-two"
	next.UpdatedAt = pending.UpdatedAt + 1
	if err := store.SaveUploadExact(next); err != nil {
		t.Fatal(err)
	}

	view := &recordingView{}
	remote := &recordingCommitRemote{}
	e := NewEngine(EngineDeps{
		Remote:        remote,
		Observer:      &fakeCommitObserver{},
		Pending:       NewStoreAdapter(store),
		Runtime:       &fakeCommitRuntime{},
		View:          view,
		Invalidations: &recordingInvalidations{store: store},
		Snapshot:      &fakeCommitSnapshot{},
		Faults:        &fakeCommitFaults{},
	})

	state, _, err := e.finalizeUpload(context.Background(), pending, drive.Entry{ID: "remote-up", Name: "up.txt", Size: 4}, Snapshot{Path: staging}, time.Now())
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if state != SnapshotStateSuperseded {
		t.Fatalf("state = %s, want superseded", state)
	}
	if len(view.commits) != 1 || view.commits[0] != "/up.txt" {
		t.Fatalf("view commits = %v, want the entry committed before the claim", view.commits)
	}
	if len(view.drops) != 1 || view.drops[0] != "/up.txt" {
		t.Fatalf("view drops = %v, want the superseded entry taken back out", view.drops)
	}
	if len(remote.removed) != 1 || remote.removed[0] != "remote-up" {
		t.Fatalf("rolled back uploads = %v, want the uploaded replacement removed", remote.removed)
	}
	if latest, ok := store.UploadByPath("/up.txt"); !ok || latest.FID != "fid-two" {
		t.Fatalf("pending = %+v, want the newer generation untouched", latest)
	}
}
