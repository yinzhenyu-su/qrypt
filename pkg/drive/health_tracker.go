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

func (t *HealthTracker) Status() HealthTrackerStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.trim()
	return t.statusLocked()
}

func (t *HealthTracker) record(op string, ok bool, message string) {
	if op == "" {
		op = "unknown"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, healthEvent{at: time.Now(), op: op, ok: ok, message: message})
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
