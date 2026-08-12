package mutation

import (
	"context"
	"errors"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// removeFixture provides stub RemoveResolver/RemoveView/DeleteScheduler/
// RemoveCleanup and records the call order.
type removeFixture struct {
	resolveEntry drive.Entry
	resolveErr   error

	pendingHandled bool
	pendingErr     error
	pendingCalls   int

	prepareErr   error
	prepareCalls int

	committed      string
	committedEntry drive.Entry
	scheduled      string
	scheduledEntry drive.Entry
	order          []string
}

func (f *removeFixture) Resolve(_ context.Context, _ string) (drive.Entry, error) {
	f.order = append(f.order, "resolve")
	return f.resolveEntry, f.resolveErr
}

func (f *removeFixture) CommitRemove(path string, entry drive.Entry) {
	f.order = append(f.order, "commit")
	f.committed = path
	f.committedEntry = entry
}

func (f *removeFixture) ScheduleDelete(path string, entry drive.Entry) {
	f.order = append(f.order, "schedule")
	f.scheduled = path
	f.scheduledEntry = entry
}

func (f *removeFixture) RemovePendingFile(_ string) (bool, error) {
	f.order = append(f.order, "pending")
	f.pendingCalls++
	return f.pendingHandled, f.pendingErr
}

func (f *removeFixture) PrepareDirectory(_ string) error {
	f.order = append(f.order, "prepare")
	f.prepareCalls++
	return f.prepareErr
}

func (f *removeFixture) coordinator() *RemoveCoordinator {
	return NewRemoveCoordinator(f, f, f, f)
}

func newRemoveFixture() *removeFixture {
	return &removeFixture{
		resolveEntry: drive.Entry{ID: "id-a", ParentID: "parent", Name: "a.txt", Size: 7},
	}
}

// TestRemoveFileSuccessCommitsThenSchedules: the success path is
// pending-probe -> resolve -> commit -> schedule, exactly once each.
func TestRemoveFileSuccessCommitsThenSchedules(t *testing.T) {
	fx := newRemoveFixture()
	if err := fx.coordinator().RemoveFile(context.Background(), "/a.txt"); err != nil {
		t.Fatal(err)
	}
	want := []string{"pending", "resolve", "commit", "schedule"}
	if len(fx.order) != len(want) {
		t.Fatalf("order = %v, want %v", fx.order, want)
	}
	for i := range want {
		if fx.order[i] != want[i] {
			t.Fatalf("order[%d] = %q, want %q", i, fx.order[i], want[i])
		}
	}
	if fx.committed != "/a.txt" || fx.scheduled != "/a.txt" {
		t.Fatalf("commit=%q schedule=%q, want /a.txt", fx.committed, fx.scheduled)
	}
	// The scheduler receives the full entry metadata.
	if fx.scheduledEntry.ID != "id-a" || fx.scheduledEntry.Size != 7 {
		t.Fatalf("scheduled entry = %+v, want full metadata", fx.scheduledEntry)
	}
}

// TestRemoveFileResolveFailureZeroSideEffects: a resolve failure commits
// and schedules nothing.
func TestRemoveFileResolveFailureZeroSideEffects(t *testing.T) {
	fx := newRemoveFixture()
	fx.resolveErr = errors.New("not found")
	if err := fx.coordinator().RemoveFile(context.Background(), "/a.txt"); err == nil {
		t.Fatal("want resolve error")
	}
	if fx.committed != "" || fx.scheduled != "" {
		t.Fatalf("side effects on resolve failure: commit=%q schedule=%q", fx.committed, fx.scheduled)
	}
}

// TestRemoveFilePendingNoRemoteCommit: a pending-only file is cleaned
// locally; no remote view commit and no remote delete schedule.
func TestRemoveFilePendingNoRemoteCommit(t *testing.T) {
	fx := newRemoveFixture()
	fx.pendingHandled = true
	if err := fx.coordinator().RemoveFile(context.Background(), "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if fx.committed != "" || fx.scheduled != "" {
		t.Fatalf("pending remove touched remote view: commit=%q schedule=%q", fx.committed, fx.scheduled)
	}
	for _, step := range fx.order {
		if step == "resolve" {
			t.Fatal("resolve called for a pending-only remove")
		}
	}
}

// TestRemoveDirectoryCleanupFailureNoSideEffects: a directory whose
// cleanup fails commits and schedules nothing (the directory stays
// visible).
func TestRemoveDirectoryCleanupFailureNoSideEffects(t *testing.T) {
	fx := newRemoveFixture()
	fx.resolveEntry = drive.Entry{ID: "id-d", IsDir: true}
	fx.prepareErr = errors.New("cleanup boom")
	if err := fx.coordinator().RemoveDirectory(context.Background(), "/dir"); err == nil {
		t.Fatal("want cleanup error")
	}
	if fx.committed != "" || fx.scheduled != "" {
		t.Fatalf("side effects on cleanup failure: commit=%q schedule=%q", fx.committed, fx.scheduled)
	}
}

// TestRemoveDirectoryNonDirRejected: a non-directory path is rejected
// before any cleanup or commit.
func TestRemoveDirectoryNonDirRejected(t *testing.T) {
	fx := newRemoveFixture()
	if err := fx.coordinator().RemoveDirectory(context.Background(), "/a.txt"); err == nil {
		t.Fatal("want not-a-directory error")
	}
	if fx.prepareCalls != 0 || fx.committed != "" || fx.scheduled != "" {
		t.Fatalf("side effects on non-dir: prepare=%d commit=%q schedule=%q", fx.prepareCalls, fx.committed, fx.scheduled)
	}
}

// TestRemoveDirectorySuccessOrder: resolve -> type check -> prepare ->
// commit -> schedule (cleanup precedes hiding the directory).
func TestRemoveDirectorySuccessOrder(t *testing.T) {
	fx := newRemoveFixture()
	fx.resolveEntry = drive.Entry{ID: "id-d", IsDir: true}
	if err := fx.coordinator().RemoveDirectory(context.Background(), "/dir"); err != nil {
		t.Fatal(err)
	}
	want := []string{"resolve", "prepare", "commit", "schedule"}
	if len(fx.order) != len(want) {
		t.Fatalf("order = %v, want %v", fx.order, want)
	}
	for i := range want {
		if fx.order[i] != want[i] {
			t.Fatalf("order[%d] = %q, want %q", i, fx.order[i], want[i])
		}
	}
	if fx.scheduledEntry.IsDir != true {
		t.Fatalf("scheduled dir entry lost IsDir: %+v", fx.scheduledEntry)
	}
}
