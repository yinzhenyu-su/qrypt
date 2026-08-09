// Package faultinject owns the debug fault-injection domain: rule
// registration, matching, firing and lifecycle. It is fully separate from
// production diagnostics state; the upload domain only depends on the
// FaultController consumer interface (defined in internal/vfs/upload), and
// pkg/vfs keeps only the public API adaptation.
package faultinject

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// DefaultTTL is the default lifetime of an injected fault.
const DefaultTTL = 10 * time.Minute

// DebugUploadCancelFault is the read-only snapshot DTO of a registered
// cancel-injection rule.
type DebugUploadCancelFault struct {
	ID          string            `json:"id"`
	Path        string            `json:"path,omitempty"`
	OpID        string            `json:"op_id,omitempty"`
	Phase       drive.UploadPhase `json:"phase,omitempty"`
	AfterBytes  int64             `json:"after_bytes,omitempty"`
	AfterDelay  string            `json:"after_delay,omitempty"`
	Once        bool              `json:"once"`
	Reason      string            `json:"reason,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	ExpiresAt   time.Time         `json:"expires_at,omitempty"`
	MatchedPath string            `json:"matched_path,omitempty"`
	Fired       bool              `json:"fired"`
	FiredAt     time.Time         `json:"fired_at,omitempty"`
}

// Fault is a registered cancel-injection rule (the registry's state).
type Fault struct {
	ID          string
	Path        string
	OpID        string
	Phase       drive.UploadPhase
	AfterBytes  int64
	AfterDelay  time.Duration
	Once        bool
	Reason      string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	MatchedPath string
	Fired       bool
	FiredAt     time.Time
	// claimed marks an in-flight once-rule claim; claimGen is the
	// monotonically increasing token for that claim (never reused, so a
	// stale Release/Fire from an old claim cannot affect a newer one).
	claimed  bool
	claimGen uint64
}

// Snapshot renders the fault as its read-only DTO.
func (f *Fault) Snapshot() DebugUploadCancelFault {
	s := DebugUploadCancelFault{
		ID:          f.ID,
		Path:        f.Path,
		OpID:        f.OpID,
		Phase:       f.Phase,
		AfterBytes:  f.AfterBytes,
		Once:        f.Once,
		Reason:      f.Reason,
		CreatedAt:   f.CreatedAt,
		ExpiresAt:   f.ExpiresAt,
		MatchedPath: f.MatchedPath,
		Fired:       f.Fired,
		FiredAt:     f.FiredAt,
	}
	if f.AfterDelay > 0 {
		s.AfterDelay = f.AfterDelay.String()
	}
	return s
}

// MatchHandle is the completion credential returned by Match: the rule's
// ID, its monotonic claim token (0 for non-once rules, which are never
// claimed), and whether the rule is one-shot. Complete/Release look the
// rule up BY ID first, then verify the token, so two rules with equal
// local tokens can never interfere.
type MatchHandle struct {
	FaultID string
	Token   uint64
	Once    bool
}

// MatchResult is an immutable snapshot of a matched rule. Callers never
// share registry-internal state; once-rules are atomically claimed during
// Match, so at most one concurrent caller wins a given once rule.
type MatchResult struct {
	ID          string
	Path        string
	OpID        string
	Phase       drive.UploadPhase
	AfterBytes  int64
	AfterDelay  time.Duration
	Once        bool
	Reason      string
	MatchedPath string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	// Handle is the completion credential for this match. For once-rules
	// it carries the claim (token); for non-once rules the token is zero
	// and Once is false. The caller must call Complete(handle) to finish,
	// or Release(handle) to return a once-claim to armed when the upload
	// ends without firing.
	Handle MatchHandle
}

// InjectRequest describes a fault to register.
type InjectRequest struct {
	Path       string
	OpID       string
	Phase      drive.UploadPhase
	AfterBytes int64
	AfterDelay time.Duration
	Once       bool
	Reason     string
	TTL        time.Duration
}

// Registry stores, matches and prunes injection rules.
type Registry struct {
	mu         sync.Mutex
	faults     map[string]*Fault
	sequence   atomic.Uint64
	defaultTTL time.Duration
}

// NewRegistry creates an empty registry; defaultTTL <= 0 uses DefaultTTL.
func NewRegistry(defaultTTL time.Duration) *Registry {
	if defaultTTL <= 0 {
		defaultTTL = DefaultTTL
	}
	return &Registry{faults: map[string]*Fault{}, defaultTTL: defaultTTL}
}

// Inject validates and registers a rule, returning its ID.
func (r *Registry) Inject(req InjectRequest) (string, error) {
	if req.Path == "" && req.OpID == "" {
		return "", fmt.Errorf("faultinject: cancel requires path or op_id")
	}
	if req.Phase == "" && req.AfterBytes <= 0 && req.AfterDelay <= 0 {
		return "", fmt.Errorf("faultinject: cancel requires phase, after_bytes or after_delay")
	}
	now := time.Now()
	if req.TTL <= 0 {
		req.TTL = r.defaultTTL
	}
	id := fmt.Sprintf("upload-cancel-%d", r.sequence.Add(1))
	fault := &Fault{
		ID:         id,
		Path:       req.Path,
		OpID:       req.OpID,
		Phase:      req.Phase,
		AfterBytes: req.AfterBytes,
		AfterDelay: req.AfterDelay,
		Once:       req.Once,
		Reason:     req.Reason,
		CreatedAt:  now,
		ExpiresAt:  now.Add(req.TTL),
	}
	r.mu.Lock()
	r.faults[id] = fault
	r.mu.Unlock()
	return id, nil
}

// Clear removes a rule by id, or every rule when id is empty.
func (r *Registry) Clear(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id == "" {
		r.faults = map[string]*Fault{}
		return nil
	}
	if _, ok := r.faults[id]; !ok {
		return fmt.Errorf("faultinject: unknown fault id %q", id)
	}
	delete(r.faults, id)
	return nil
}

// Faults returns snapshots of all rules, pruning expired ones first.
func (r *Registry) Faults(now time.Time) []DebugUploadCancelFault {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneExpiredLocked(now)
	out := make([]DebugUploadCancelFault, 0, len(r.faults))
	for _, fault := range r.faults {
		out = append(out, fault.Snapshot())
	}
	return out
}

// Match returns the first rule matching BOTH non-empty path and op_id
// constraints, pruning expired rules and skipping fired once-rules. The
// result is an immutable value copy. A once-rule is claimed (not
// consumed): exactly one concurrent caller wins the claim, and the rule
// stays claimed until the winner calls Fire (permanent) or Release
// (back to armed).
func (r *Registry) Match(now time.Time, path, opID string) (MatchResult, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneExpiredLocked(now)
	for _, fault := range r.faults {
		if fault.Fired && fault.Once {
			continue
		}
		if fault.Once && fault.claimed {
			continue // already claimed by another upload
		}
		if fault.Path != "" && fault.Path != path {
			continue
		}
		if fault.OpID != "" && fault.OpID != opID {
			continue
		}
		result := MatchResult{
			ID:          fault.ID,
			Path:        fault.Path,
			OpID:        fault.OpID,
			Phase:       fault.Phase,
			AfterBytes:  fault.AfterBytes,
			AfterDelay:  fault.AfterDelay,
			Once:        fault.Once,
			Reason:      fault.Reason,
			MatchedPath: path,
			CreatedAt:   fault.CreatedAt,
			ExpiresAt:   fault.ExpiresAt,
		}
		result.Handle = MatchHandle{FaultID: fault.ID, Once: fault.Once}
		if fault.Once {
			// Claim the rule: while claimed it cannot be matched again.
			// claimGen never resets, so tokens are never reused.
			fault.claimed = true
			fault.claimGen++
			result.Handle.Token = fault.claimGen
		}
		return result, true
	}
	return MatchResult{}, false
}

// Complete finishes a match: for once-rules it permanently consumes the
// rule (never matchable again); for non-once rules it records Fired/
// FiredAt while keeping the rule registered. The rule is looked up by
// handle.FaultID, the handle must match the fault's own Once-ness, and
// for once-rules the claim token must be the current one - a handle from
// another rule (even with an equal token) can never consume this rule.
func (r *Registry) Complete(handle MatchHandle, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fault, ok := r.faults[handle.FaultID]
	if !ok {
		return
	}
	if fault.Once {
		if handle.Once && fault.claimed && fault.claimGen == handle.Token {
			delete(r.faults, handle.FaultID)
		}
		return
	}
	fault.Fired = true
	fault.FiredAt = now
}

// Release returns a once-rule claim to armed so a later upload can match
// it again. The rule is looked up by handle.FaultID and the token
// verified, so a stale cleanup can never release a claim re-issued to a
// newer upload.
func (r *Registry) Release(handle MatchHandle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fault, ok := r.faults[handle.FaultID]
	if !ok || !fault.Once || !handle.Once || !fault.claimed || fault.claimGen != handle.Token {
		return
	}
	fault.claimed = false
}

// pruneExpiredLocked removes expired rules (r.mu held).
func (r *Registry) pruneExpiredLocked(now time.Time) {
	for id, fault := range r.faults {
		if fault.ExpiresAt.Before(now) {
			delete(r.faults, id)
		}
	}
}
