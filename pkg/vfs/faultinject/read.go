package faultinject

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/util"
)

// Read fault kinds. Each one exercises a different recovery path in the
// shared read layer:
//
//   - stall: stop producing bytes so the idle timeout fires and the window is
//     replayed on a fresh request.
//   - truncate: end the body cleanly before its declared range so the short
//     body replay path runs.
//   - status: fail the Range GET with an HTTP status so the retryable-status
//     backoff runs.
//   - delay: delay the response body's first byte so open latency is visible.
const (
	ReadFaultStall    = "stall"
	ReadFaultTruncate = "truncate"
	ReadFaultStatus   = "status"
	ReadFaultDelay    = "delay"
)

// DebugReadFaultRequest describes one read fault to inject. Path is a virtual
// path; the VFS resolves it to EntryID before the rule is armed.
type DebugReadFaultRequest struct {
	ID         string `json:"id,omitempty"`
	Path       string `json:"path,omitempty"`
	EntryID    string `json:"entry_id,omitempty"`
	Kind       string `json:"kind"`
	Offset     int64  `json:"offset,omitempty"`
	Length     int64  `json:"length,omitempty"`
	AnyOffset  bool   `json:"any_offset,omitempty"`
	AfterBytes int64  `json:"after_bytes,omitempty"`
	Delay      string `json:"delay,omitempty"`
	Status     int    `json:"status,omitempty"`
	Times      int    `json:"times,omitempty"`
	// IncludePrefetch lets a rule fire on a background prefetch window. The
	// default is false so a canceled speculative prefetch cannot consume a
	// rule meant for the foreground read.
	IncludePrefetch bool   `json:"include_prefetch,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// DebugReadFault is one armed or recently fired read fault.
type DebugReadFault struct {
	ID              string    `json:"id"`
	Path            string    `json:"path,omitempty"`
	EntryID         string    `json:"entry_id,omitempty"`
	Kind            string    `json:"kind"`
	Offset          int64     `json:"offset"`
	Length          int64     `json:"length,omitempty"`
	AnyOffset       bool      `json:"any_offset,omitempty"`
	AfterBytes      int64     `json:"after_bytes,omitempty"`
	DelayMS         int64     `json:"delay_ms,omitempty"`
	Status          int       `json:"status,omitempty"`
	Times           int       `json:"times"`
	Remaining       int       `json:"remaining"`
	IncludePrefetch bool      `json:"include_prefetch,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// ReadFaultRegistry stores read fault rules. A rule fires once by default; a
// stalled or truncated body consumes the rule on the first attempt so the
// replay sees a healthy provider.
type ReadFaultRegistry struct {
	mu     sync.Mutex
	rules  map[string]*DebugReadFault
	order  []string
	nextID atomic.Uint64
}

func NewReadFaultRegistry() *ReadFaultRegistry {
	return &ReadFaultRegistry{rules: map[string]*DebugReadFault{}}
}

// Inject arms a read fault. It validates the kind and its required fields so a
// typo fails at the command instead of silently never firing.
func (r *ReadFaultRegistry) Inject(req DebugReadFaultRequest) (DebugReadFault, error) {
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind == "" {
		return DebugReadFault{}, fmt.Errorf("read fault kind required")
	}
	if req.Path == "" && req.EntryID == "" {
		return DebugReadFault{}, fmt.Errorf("read fault requires path or entry_id")
	}
	fault := &DebugReadFault{
		ID:              strings.TrimSpace(req.ID),
		Path:            req.Path,
		EntryID:         req.EntryID,
		Kind:            kind,
		Offset:          req.Offset,
		Length:          req.Length,
		AnyOffset:       req.AnyOffset,
		AfterBytes:      req.AfterBytes,
		Status:          req.Status,
		Times:           req.Times,
		IncludePrefetch: req.IncludePrefetch,
		Reason:          req.Reason,
		CreatedAt:       util.Now(),
	}
	if fault.Offset < 0 || fault.Length < 0 || fault.AfterBytes < 0 {
		return DebugReadFault{}, fmt.Errorf("read fault offset, length, and after_bytes must be non-negative")
	}
	if fault.Times <= 0 {
		fault.Times = 1
	}
	fault.Remaining = fault.Times
	switch kind {
	case ReadFaultStall, ReadFaultTruncate:
		if req.AfterBytes < 0 {
			return DebugReadFault{}, fmt.Errorf("read fault after_bytes must be non-negative")
		}
	case ReadFaultStatus:
		if fault.Status == 0 {
			fault.Status = 500
		}
		if fault.Status < 100 || fault.Status > 599 {
			return DebugReadFault{}, fmt.Errorf("read fault status must be between 100 and 599")
		}
	case ReadFaultDelay:
		delay, err := time.ParseDuration(strings.TrimSpace(req.Delay))
		if err != nil || delay <= 0 {
			return DebugReadFault{}, fmt.Errorf("read fault delay must be a positive duration")
		}
		fault.DelayMS = delay.Milliseconds()
		if fault.DelayMS <= 0 {
			fault.DelayMS = 1
		}
	default:
		return DebugReadFault{}, fmt.Errorf("read fault kind must be stall, truncate, status, or delay")
	}
	if fault.ID == "" {
		fault.ID = fmt.Sprintf("read-fault-%d-%d", util.Now().UnixNano(), r.nextID.Add(1))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.rules[fault.ID]; !exists {
		r.order = append(r.order, fault.ID)
	}
	copied := *fault
	r.rules[fault.ID] = &copied
	return copied, nil
}

// Faults returns every rule, oldest first, including rules whose remaining
// count reached zero.
func (r *ReadFaultRegistry) Faults() []DebugReadFault {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]DebugReadFault, 0, len(r.order))
	for _, id := range r.order {
		if fault, ok := r.rules[id]; ok {
			out = append(out, *fault)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// Clear removes one rule, or every rule when id is empty.
func (r *ReadFaultRegistry) Clear(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id == "" {
		r.rules = map[string]*DebugReadFault{}
		r.order = nil
		return
	}
	delete(r.rules, id)
	for index, candidate := range r.order {
		if candidate == id {
			r.order = append(r.order[:index], r.order[index+1:]...)
			break
		}
	}
}

// MatchReadFault reports whether a rule should fire for this range without
// consuming it. The caller consumes the rule once the fault actually happens,
// so a prefetch body that is closed before its first read cannot swallow a
// rule meant for the foreground.
func (r *ReadFaultRegistry) MatchReadFault(entryID string, offset, size int64) (DebugReadFault, bool) {
	if r == nil || entryID == "" {
		return DebugReadFault{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range r.order {
		fault, ok := r.rules[id]
		if !ok || fault.Remaining <= 0 {
			continue
		}
		if fault.EntryID != "" && fault.EntryID != entryID {
			continue
		}
		if !fault.AnyOffset && !faultMatchesRange(fault, offset, size) {
			continue
		}
		return *fault, true
	}
	return DebugReadFault{}, false
}

// ConsumeReadFault spends one use of a matched rule.
func (r *ReadFaultRegistry) ConsumeReadFault(id string) {
	if r == nil || id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if fault, ok := r.rules[id]; ok && fault.Remaining > 0 {
		fault.Remaining--
	}
}

func faultMatchesRange(fault *DebugReadFault, offset, size int64) bool {
	if size <= 0 {
		size = 1
	}
	end := offset + size
	if fault.Length > 0 {
		return fault.Offset < end && offset < fault.Offset+fault.Length
	}
	return offset <= fault.Offset && fault.Offset < end
}
