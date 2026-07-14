package drive

import (
	"sync"
	"time"
)

const (
	DefaultHealthWindow = 5 * time.Minute
	DefaultMaxEvents    = 200

	HealthOpList   = "list"
	HealthOpStat   = "stat"
	HealthOpRead   = "read"
	HealthOpCreate = "create"
	HealthOpWrite  = "write"
	HealthOpUpload = "upload"
	HealthOpMkdir  = "mkdir"
	HealthOpRename = "rename"
	HealthOpDelete = "delete"
	HealthOpSpace  = "space"
)

type healthEvent struct {
	at      time.Time
	op      string
	ok      bool
	message string
}

type HealthTracker struct {
	mu        sync.Mutex
	events    []healthEvent
	window    time.Duration
	maxEvents int
}

func NewHealthTracker(window time.Duration, maxEvents int) *HealthTracker {
	if window <= 0 {
		window = DefaultHealthWindow
	}
	if maxEvents <= 0 {
		maxEvents = DefaultMaxEvents
	}
	return &HealthTracker{
		window:    window,
		maxEvents: maxEvents,
	}
}

func (t *HealthTracker) RecordSuccess(op string) {
	t.record(op, true, "")
}

func (t *HealthTracker) RecordError(op string, err error) {
	message := ""
	if err != nil {
		message = err.Error()
	}
	t.record(op, false, message)
}

func (t *HealthTracker) RecordResult(op string, err error) {
	if err == nil {
		t.RecordSuccess(op)
	} else {
		t.RecordError(op, err)
	}
}

func HealthStatusFromMetrics(metrics []MetricEvent, window time.Duration, maxEvents int) HealthTrackerStatus {
	tracker := NewHealthTracker(window, maxEvents)
	for _, metric := range metrics {
		op := healthMetricOp(metric)
		ok := metric.OK && metric.Error == ""
		tracker.recordAt(metric.At, op, ok, metric.Error)
	}
	return tracker.Status()
}

func MergeHealthStatus(a, b HealthTrackerStatus) HealthTrackerStatus {
	if a.CheckedAt.IsZero() {
		return b
	}
	if b.CheckedAt.IsZero() {
		return a
	}
	merged := HealthTrackerStatus{
		OK:        true,
		Level:     HealthLevelOK,
		CheckedAt: a.CheckedAt,
		Ops:       map[string]HealthOpStatus{},
	}
	if b.CheckedAt.After(merged.CheckedAt) {
		merged.CheckedAt = b.CheckedAt
	}
	mergeHealthOps(merged.Ops, a.Ops)
	mergeHealthOps(merged.Ops, b.Ops)
	for _, op := range merged.Ops {
		merged.Success += op.Success
		merged.Errors += op.Errors
	}
	merged.Level = healthLevel(merged.Errors)
	merged.OK = merged.Level == HealthLevelOK
	if !merged.OK {
		merged.Error = "health degraded by recent operation failures"
	}
	return merged
}

func (t *HealthTracker) Status() HealthTrackerStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.trim()
	return t.statusLocked()
}

func (t *HealthTracker) record(op string, ok bool, message string) {
	t.recordAt(time.Now(), op, ok, message)
}

func (t *HealthTracker) recordAt(at time.Time, op string, ok bool, message string) {
	if op == "" {
		op = "unknown"
	}
	if at.IsZero() {
		at = time.Now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, healthEvent{at: at, op: op, ok: ok, message: message})
	t.trim()
}

func (t *HealthTracker) statusLocked() HealthTrackerStatus {
	status := HealthTrackerStatus{
		OK:        true,
		Level:     HealthLevelOK,
		CheckedAt: time.Now(),
		Ops:       map[string]HealthOpStatus{},
	}
	for _, event := range t.events {
		op := status.Ops[event.op]
		if event.ok {
			op.Success++
		} else {
			op.Errors++
			op.LastError = event.message
			op.LastErrorAt = event.at
		}
		status.Ops[event.op] = op
	}
	for _, op := range status.Ops {
		status.Success += op.Success
		status.Errors += op.Errors
	}
	status.Level = healthLevel(status.Errors)
	status.OK = status.Level == HealthLevelOK
	if !status.OK {
		status.Error = "health degraded by recent operation failures"
	}
	return status
}

func healthLevel(errors int) string {
	switch {
	case errors == 0:
		return HealthLevelOK
	case errors < 5:
		return HealthLevelDegraded
	default:
		return HealthLevelUnhealthy
	}
}

func healthMetricOp(metric MetricEvent) string {
	for _, value := range []string{metric.Operation, metric.Step, metric.Phase, metric.Name} {
		if value != "" {
			if isHighCardinalityOperation(value) {
				return "api_request"
			}
			return value
		}
	}
	return "unknown"
}

func mergeHealthOps(dst map[string]HealthOpStatus, src map[string]HealthOpStatus) {
	for name, status := range src {
		current := dst[name]
		current.Success += status.Success
		current.Errors += status.Errors
		if status.LastError != "" && (current.LastErrorAt.IsZero() || status.LastErrorAt.After(current.LastErrorAt)) {
			current.LastError = status.LastError
			current.LastErrorAt = status.LastErrorAt
		}
		dst[name] = current
	}
}

func (t *HealthTracker) trim() {
	cutoff := time.Now().Add(-t.window)
	keep := t.events[:0]
	for _, e := range t.events {
		if e.at.After(cutoff) {
			keep = append(keep, e)
		}
	}
	t.events = keep

	if len(t.events) > t.maxEvents {
		t.events = t.events[len(t.events)-t.maxEvents:]
	}
}

type HealthTrackerStatus struct {
	OK        bool
	Level     string
	CheckedAt time.Time
	Error     string
	Success   int
	Errors    int
	Ops       map[string]HealthOpStatus
}

type HealthOpStatus struct {
	Success     int
	Errors      int
	LastError   string
	LastErrorAt time.Time
}
