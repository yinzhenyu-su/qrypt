package diagnostics

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/internal/timeutil"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// MountHealthOp summarizes one operation type's health counters.
type MountHealthOp struct {
	Success     int       `json:"success"`
	Errors      int       `json:"errors"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitempty"`
}

// MountHealth is the per-mount runtime health report.
type MountHealth struct {
	Mount     string                   `json:"mount"`
	OK        bool                     `json:"ok"`
	Level     string                   `json:"level,omitempty"`
	Error     string                   `json:"error,omitempty"`
	CheckedAt time.Time                `json:"checked_at"`
	Success   int                      `json:"success"`
	Errors    int                      `json:"errors"`
	Ops       map[string]MountHealthOp `json:"ops,omitempty"`
}

// HealthRuntime is the health diagnostic surface (consumer side): the
// mount's tracker status and the driver's metric events for the window.
type HealthRuntime interface {
	Status() drive.HealthTrackerStatus
	DriverMetrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error)
}

// AssembleHealth builds one mount's health report: tracker status merged
// with driver metrics over the default window.
func AssembleHealth(ctx context.Context, mountName string, runtime HealthRuntime) MountHealth {
	now := timeutil.Now()
	h := MountHealth{Mount: mountName, CheckedAt: now}
	result := runtime.Status()
	if metrics, err := runtime.DriverMetrics(ctx, now.Add(-drive.DefaultHealthWindow)); err == nil {
		driverHealth := drive.HealthStatusFromMetrics(metrics, drive.DefaultHealthWindow, drive.DefaultMaxEvents)
		result = drive.MergeHealthStatus(result, driverHealth)
	}
	h.OK = result.OK
	h.Level = result.Level
	h.Error = result.Error
	h.CheckedAt = result.CheckedAt
	h.Success = result.Success
	h.Errors = result.Errors
	if len(result.Ops) > 0 {
		h.Ops = map[string]MountHealthOp{}
		for op, status := range result.Ops {
			h.Ops[op] = MountHealthOp{
				Success:     status.Success,
				Errors:      status.Errors,
				LastError:   status.LastError,
				LastErrorAt: status.LastErrorAt,
			}
		}
	}
	return h
}
