package faultinject

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// fakeTimer is a controllable timer: Stop is recorded, fire runs the
// callback synchronously.
type fakeTimer struct {
	fn      func()
	stopped bool
}

func (t *fakeTimer) fire() {
	if t.fn != nil {
		t.fn()
	}
}

func (t *fakeTimer) Stop() bool {
	t.stopped = true
	return true
}

type fakeTimerFactory struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

func (f *fakeTimerFactory) newTimer(_ time.Duration, fn func()) TimerHandle {
	t := &fakeTimer{fn: fn}
	f.mu.Lock()
	f.timers = append(f.timers, t)
	f.mu.Unlock()
	return t
}

func (f *fakeTimerFactory) all() []*fakeTimer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeTimer(nil), f.timers...)
}

type recordingProgress struct {
	phases   []drive.UploadPhase
	uploaded int64
}

func (r *recordingProgress) Phase(p drive.UploadPhase) { r.phases = append(r.phases, p) }
func (r *recordingProgress) Uploaded(n int64)          { r.uploaded += n }

// fixture claims one rule and builds a Progress over it with a
// controllable timer factory.
func fixture(t *testing.T, req InjectRequest) (*Registry, *fakeTimerFactory, *Progress, *context.Context) {
	t.Helper()
	reg := NewRegistry(0)
	if _, err := reg.Inject(req); err != nil {
		t.Fatal(err)
	}
	match, ok := reg.Match(time.Now(), req.Path, req.OpID)
	if !ok {
		t.Fatal("match failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	factory := &fakeTimerFactory{}
	progress := reg.newProgress(match, &recordingProgress{}, cancel, factory.newTimer)
	// Keep cancel reachable via the context: fire cancels it.
	_ = cancel
	return reg, factory, progress, &ctx
}

// TestProgressFiresOnPhase: a phase-only rule fires when the phase
// arrives and consumes the once-rule.
func TestProgressFiresOnPhase(t *testing.T) {
	reg, _, progress, ctx := fixture(t, InjectRequest{Path: "/f.txt", Phase: "uploading", Once: true})
	progress.Phase(drive.UploadPhaseUploading)
	if (*ctx).Err() == nil {
		t.Fatal("fire did not cancel the upload context")
	}
	progress.Close()
	if _, ok := reg.Match(time.Now(), "/f.txt", ""); ok {
		t.Fatal("once-rule still matchable after firing")
	}
}

// TestProgressBytesThreshold: fires only after enough bytes.
func TestProgressBytesThreshold(t *testing.T) {
	reg, _, progress, ctx := fixture(t, InjectRequest{Path: "/f.txt", AfterBytes: 10, Once: true})
	progress.Uploaded(5)
	if (*ctx).Err() != nil {
		t.Fatal("cancelled before threshold")
	}
	progress.Uploaded(5) // reaches threshold: fires
	if (*ctx).Err() == nil {
		t.Fatal("did not cancel at threshold")
	}
	progress.Close()
	if _, ok := reg.Match(time.Now(), "/f.txt", ""); ok {
		t.Fatal("once-rule still matchable after firing")
	}
}

// TestProgressCloseThenLateFireIsStale: Close releases the claim; a late
// timer callback must neither fire nor consume.
func TestProgressCloseThenLateFireIsStale(t *testing.T) {
	reg, factory, progress, ctx := fixture(t, InjectRequest{Path: "/f.txt", Phase: "uploading", AfterDelay: time.Hour, Once: true})
	progress.Phase(drive.UploadPhaseUploading) // arms the delay timer
	if got := len(factory.all()); got != 1 {
		t.Fatalf("timers = %d, want 1", got)
	}
	progress.Close()
	factory.all()[0].fire() // late timer callback after Close
	if (*ctx).Err() != nil {
		t.Fatal("late fire after Close cancelled the upload")
	}
	// Rule was released, so a new upload can match it again.
	if _, ok := reg.Match(time.Now(), "/f.txt", ""); !ok {
		t.Fatal("rule not re-armed after Close")
	}
}

// TestProgressFireThenCloseDoesNotRelease: after firing, Close must not
// release the claim - the once-rule stays consumed.
func TestProgressFireThenCloseDoesNotRelease(t *testing.T) {
	reg, _, progress, ctx := fixture(t, InjectRequest{Path: "/f.txt", AfterBytes: 1, Once: true})
	progress.Uploaded(1) // fires
	if (*ctx).Err() == nil {
		t.Fatal("fire did not cancel")
	}
	progress.Close()
	if _, ok := reg.Match(time.Now(), "/f.txt", ""); ok {
		t.Fatal("once-rule re-armed after fire+Close")
	}
}

// TestProgressNonOnceRecordsFired: a repeating rule records fired state
// on the registry but stays matchable.
func TestProgressNonOnceRecordsFired(t *testing.T) {
	reg, _, progress, _ := fixture(t, InjectRequest{Path: "/f.txt", AfterBytes: 1, Once: false})
	progress.Uploaded(1) // fires (non-once: no claim to consume)
	progress.Close()
	faults := reg.Faults(time.Now())
	if len(faults) != 1 || !faults[0].Fired {
		t.Fatalf("repeating rule should stay registered and show fired: %+v", faults)
	}
	if _, ok := reg.Match(time.Now(), "/f.txt", ""); !ok {
		t.Fatal("repeating rule should still match after firing")
	}
}

// TestProgressDelayTimerFires: an AfterDelay rule fires when the timer
// runs, and Close before the delay releases the claim.
func TestProgressDelayTimerFires(t *testing.T) {
	reg, factory, progress, ctx := fixture(t, InjectRequest{Path: "/f.txt", Phase: "uploading", AfterDelay: time.Hour, Once: true})
	progress.Phase(drive.UploadPhaseUploading)
	factory.all()[0].fire()
	if (*ctx).Err() == nil {
		t.Fatal("delay timer fire did not cancel")
	}
	progress.Close()
	if _, ok := reg.Match(time.Now(), "/f.txt", ""); ok {
		t.Fatal("once-rule still matchable after delay fire")
	}
}
