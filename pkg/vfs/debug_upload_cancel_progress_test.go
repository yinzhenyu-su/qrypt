package vfs

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestProgressCloseThenLateFireIsStale: after Close releases the claim, a
// late timer callback must not fire the cancellation nor consume the rule
// again - the rule stays armed for a later upload.
func TestProgressCloseThenLateFireIsStale(t *testing.T) {
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close(context.Background())

	ctx := context.Background()
	if _, err := fs.DebugInjectUploadCancel(ctx, DebugUploadCancelRequest{
		Path:  "/x.txt",
		Phase: drive.UploadPhaseUploading,
	}); err != nil {
		t.Fatal(err)
	}
	fault, ok := fs.matchUploadCancelFault("/x.txt", "fid-x")
	if !ok {
		t.Fatal("rule not matched")
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	fired := false
	progress := &debugUploadCancelProgress{
		fault:      fault,
		cancel:     func() { fired = true; cancel() },
		cancelPath: "/x.txt",
		cancelOpID: "fid-x",
		v:          fs,
	}

	progress.Close() // release the claim

	// Late timer callback after Close: must be a no-op.
	progress.mu.Lock()
	progress.fireLocked()
	progress.mu.Unlock()
	if fired {
		t.Fatal("late fire after Close cancelled the upload")
	}
	if cancelCtx.Err() != nil {
		t.Fatal("late fire after Close cancelled the context")
	}
	// The rule was released, so a new upload can match it again.
	if _, ok := fs.matchUploadCancelFault("/x.txt", "fid-x2"); !ok {
		t.Fatal("rule not re-armed after Close")
	}
}

// TestProgressFireThenCloseDoesNotRelease: after the timer fires, Close
// must not release the claim - the once-rule stays consumed.
func TestProgressFireThenCloseDoesNotRelease(t *testing.T) {
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close(context.Background())

	ctx := context.Background()
	if _, err := fs.DebugInjectUploadCancel(ctx, DebugUploadCancelRequest{
		Path:  "/x.txt",
		Phase: drive.UploadPhaseUploading,
	}); err != nil {
		t.Fatal(err)
	}
	fault, ok := fs.matchUploadCancelFault("/x.txt", "fid-x")
	if !ok {
		t.Fatal("rule not matched")
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	progress := &debugUploadCancelProgress{
		fault:      fault,
		cancel:     cancel,
		cancelPath: "/x.txt",
		cancelOpID: "fid-x",
		v:          fs,
	}

	progress.mu.Lock()
	progress.fireLocked()
	progress.mu.Unlock()
	if cancelCtx.Err() == nil {
		t.Fatal("fire did not cancel the upload context")
	}

	progress.Close() // must NOT release the consumed rule
	if _, ok := fs.matchUploadCancelFault("/x.txt", "fid-x3"); ok {
		t.Fatal("once-rule re-armed after fire+Close")
	}
}

// TestProgressDelayedFireSerializedWithClose: with an AfterDelay, the
// timer callback and Close race on the same lock - exactly one of fire /
// release wins, and the rule ends in a consistent state.
func TestProgressDelayedFireSerializedWithClose(t *testing.T) {
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close(context.Background())

	ctx := context.Background()
	if _, err := fs.DebugInjectUploadCancel(ctx, DebugUploadCancelRequest{
		Path:       "/x.txt",
		Phase:      drive.UploadPhaseUploading,
		AfterDelay: time.Hour, // never reached; upload ends first
	}); err != nil {
		t.Fatal(err)
	}
	fault, ok := fs.matchUploadCancelFault("/x.txt", "fid-x")
	if !ok {
		t.Fatal("rule not matched")
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	progress := &debugUploadCancelProgress{
		fault:      fault,
		cancel:     cancel,
		cancelPath: "/x.txt",
		cancelOpID: "fid-x",
		v:          fs,
	}
	// Arm the delay timer through the normal path (phase must match the
	// fault's phase first).
	progress.mu.Lock()
	progress.phase = drive.UploadPhaseUploading
	progress.maybeCancelLocked()
	progress.mu.Unlock()
	if progress.timer == nil {
		t.Fatal("delay timer not armed")
	}

	// Upload ends: Close stops the timer and releases the claim.
	progress.Close()
	if cancelCtx.Err() != nil {
		t.Fatal("close must not cancel before the delay elapses")
	}
	// The rule must be re-armed (threshold never reached).
	if _, ok := fs.matchUploadCancelFault("/x.txt", "fid-y"); !ok {
		t.Fatal("rule not re-armed after delayed upload ended")
	}
}
