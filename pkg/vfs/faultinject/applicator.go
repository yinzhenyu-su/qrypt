package faultinject

import (
	"context"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// TimerHandle is the minimal timer surface the applicator needs.
type TimerHandle interface {
	Stop() bool
}

// NewTimerFunc creates a timer; production uses time.AfterFunc, tests
// inject a controllable fake (no real-time waits).
type NewTimerFunc func(delay time.Duration, fn func()) TimerHandle

// Progress applies a matched cancel-injection rule to one upload: it
// wraps the upload progress, tracks phase and bytes, arms the delay
// timer when AfterDelay is set, fires the cancellation when the rule's
// threshold is reached, and reports completion/release back to the
// Registry. It owns no rule lifecycle beyond its own claim.
type Progress struct {
	reg      *Registry
	inner    drive.UploadProgress
	result   MatchResult
	cancel   context.CancelFunc
	newTimer NewTimerFunc

	mu         sync.Mutex
	bytes      int64
	phase      drive.UploadPhase
	timer      TimerHandle
	timerArmed bool
	fired      bool
}

// NewProgress wraps a matched rule for one upload. cancel is invoked
// when the rule fires. The returned progress must be Closed when the
// upload ends.
func (r *Registry) NewProgress(result MatchResult, inner drive.UploadProgress, cancel context.CancelFunc) *Progress {
	return r.newProgress(result, inner, cancel, func(d time.Duration, fn func()) TimerHandle {
		return time.AfterFunc(d, fn)
	})
}

func (r *Registry) newProgress(result MatchResult, inner drive.UploadProgress, cancel context.CancelFunc, newTimer NewTimerFunc) *Progress {
	return &Progress{reg: r, inner: inner, result: result, cancel: cancel, newTimer: newTimer}
}

// Phase implements drive.UploadProgress and checks the rule's phase
// threshold.
func (p *Progress) Phase(phase drive.UploadPhase) {
	if p.inner != nil {
		p.inner.Phase(phase)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase = phase
	p.maybeCancelLocked()
}

// Uploaded implements drive.UploadProgress and checks the rule's byte
// threshold.
func (p *Progress) Uploaded(n int64) {
	if p.inner != nil {
		p.inner.Uploaded(n)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if n > 0 {
		p.bytes += n
	}
	p.maybeCancelLocked()
}

// Close ends the upload. It stops the delay timer and, when the rule did
// not fire, returns the claim to armed so a later upload can still
// trigger it. It also blocks any late timer callback from firing.
func (p *Progress) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.timer != nil {
		p.timer.Stop()
	}
	if !p.fired {
		p.fired = true
		p.reg.Release(p.result.Handle)
	}
}

// maybeCancelLocked requires p.mu to be held.
func (p *Progress) maybeCancelLocked() {
	if p.result.ID == "" || p.fired {
		return
	}
	if p.result.Phase != "" && p.phase != p.result.Phase {
		return
	}
	if p.result.AfterBytes > 0 && p.bytes < p.result.AfterBytes {
		return
	}
	if p.result.AfterDelay > 0 {
		if p.timerArmed {
			return
		}
		p.timerArmed = true
		p.timer = p.newTimer(p.result.AfterDelay, func() {
			// fireLocked is serialized against Phase/Uploaded/Close.
			p.mu.Lock()
			p.fireLocked()
			p.mu.Unlock()
		})
		return
	}
	p.fireLocked()
}

// fireLocked requires p.mu to be held. It is the single completion path:
// the registry records the fired state via Complete.
func (p *Progress) fireLocked() {
	if p.fired {
		return
	}
	p.fired = true
	logging.L.Warnf("[VFS] debug upload cancel fired fault=%q path=%q op_id=%q reason=%q", p.result.ID, p.result.Path, p.result.OpID, p.result.Reason)
	p.reg.Complete(p.result.Handle, util.Now())
	if p.cancel != nil {
		p.cancel()
	}
}
