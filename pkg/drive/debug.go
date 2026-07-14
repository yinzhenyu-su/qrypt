package drive

import (
	"context"
	"strings"
	"time"
)

type DebugSnapshot struct {
	Driver      string         `json:"driver"`
	Health      string         `json:"health"`
	GeneratedAt time.Time      `json:"generated_at"`
	Stats       map[string]any `json:"stats,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

type debugOperationContextKey struct{}

type DebugOperation struct {
	OpID string `json:"op_id,omitempty"`
	Step string `json:"step,omitempty"`
	Name string `json:"name,omitempty"`
}

func WithDebugOperation(ctx context.Context, op DebugOperation) context.Context {
	return context.WithValue(ctx, debugOperationContextKey{}, op)
}

func DebugOperationFromContext(ctx context.Context) (DebugOperation, bool) {
	op, ok := ctx.Value(debugOperationContextKey{}).(DebugOperation)
	return op, ok
}

type DebugTraceEvent struct {
	At              time.Time      `json:"at"`
	OpID            string         `json:"op_id,omitempty"`
	Step            string         `json:"step,omitempty"`
	Name            string         `json:"name,omitempty"`
	Layer           string         `json:"layer,omitempty"`
	Operation       string         `json:"operation,omitempty"`
	Method          string         `json:"method,omitempty"`
	URL             string         `json:"url,omitempty"`
	Status          int            `json:"status,omitempty"`
	Duration        string         `json:"duration,omitempty"`
	Request         map[string]any `json:"request,omitempty"`
	Response        map[string]any `json:"response,omitempty"`
	Error           string         `json:"error,omitempty"`
	Attempt         int            `json:"attempt,omitempty"`
	Retry           bool           `json:"retry,omitempty"`
	SensitiveMasked bool           `json:"sensitive_masked,omitempty"`
}

type MetricEvent struct {
	At              time.Time      `json:"at"`
	OpID            string         `json:"op_id,omitempty"`
	Step            string         `json:"step,omitempty"`
	Name            string         `json:"name,omitempty"`
	Layer           string         `json:"layer,omitempty"`
	Driver          string         `json:"driver,omitempty"`
	Operation       string         `json:"operation,omitempty"`
	Phase           string         `json:"phase,omitempty"`
	OK              bool           `json:"ok"`
	Duration        string         `json:"duration,omitempty"`
	DurationMS      int64          `json:"duration_ms,omitempty"`
	Bytes           int64          `json:"bytes,omitempty"`
	Throughput      int64          `json:"throughput,omitempty"`
	Attempts        int            `json:"attempts,omitempty"`
	Error           string         `json:"error,omitempty"`
	ErrorCategory   string         `json:"error_category,omitempty"`
	SensitiveMasked bool           `json:"sensitive_masked,omitempty"`
	Extra           map[string]any `json:"extra,omitempty"`
}

func MetricsFromTrace(driver string, trace []DebugTraceEvent) []MetricEvent {
	if len(trace) == 0 {
		return nil
	}
	events := make([]MetricEvent, 0, len(trace))
	for _, event := range trace {
		duration, _ := time.ParseDuration(event.Duration)
		metric := MetricEvent{
			At:              event.At,
			OpID:            event.OpID,
			Step:            event.Step,
			Name:            event.Name,
			Layer:           event.Layer,
			Driver:          driver,
			Operation:       metricOperation(event),
			Phase:           event.Method,
			OK:              event.Error == "",
			Duration:        event.Duration,
			DurationMS:      durationMillis(duration),
			Error:           event.Error,
			ErrorCategory:   ErrorCategoryMessage(event.Error),
			SensitiveMasked: event.SensitiveMasked,
			Extra:           traceMetricExtra(event),
		}
		if event.Attempt > 0 {
			metric.Attempts = event.Attempt
		}
		events = append(events, metric)
	}
	return events
}

func metricOperation(event DebugTraceEvent) string {
	op := firstNonEmpty(event.Operation, event.Step, event.Name)
	if op == "" {
		if event.Method != "" {
			return "api_request"
		}
		return ""
	}
	if isHighCardinalityOperation(op) {
		return "api_request"
	}
	return op
}

func isHighCardinalityOperation(op string) bool {
	op = strings.TrimSpace(op)
	if op == "" {
		return false
	}
	if strings.HasPrefix(op, "/") || strings.HasPrefix(op, "http://") || strings.HasPrefix(op, "https://") {
		return true
	}
	if strings.ContainsAny(op, "?#=&") {
		return true
	}
	return false
}

func traceMetricExtra(event DebugTraceEvent) map[string]any {
	extra := map[string]any{}
	if event.Operation != "" && isHighCardinalityOperation(event.Operation) {
		extra["raw_operation"] = event.Operation
	}
	if event.URL != "" {
		extra["url"] = event.URL
	}
	if event.Status != 0 {
		extra["status"] = event.Status
	}
	if event.Retry {
		extra["retry"] = true
	}
	if len(event.Request) > 0 {
		extra["request"] = event.Request
	}
	if len(event.Response) > 0 {
		extra["response"] = event.Response
	}
	if len(extra) == 0 {
		return nil
	}
	return extra
}

func durationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Millisecond - 1) / time.Millisecond)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

const (
	HealthLevelOK        = "ok"
	HealthLevelDegraded  = "degraded"
	HealthLevelUnhealthy = "unhealthy"
)

const (
	DebugExtraInstantUploadCount     = "instant_upload_count"
	DebugExtraLegacyRapidUploadCount = "rapid_upload_count"
	DebugExtraCredentialSource       = "credential_source"
	DebugExtraCredentialUpdated      = "credential_updated"
	DebugExtraLastError              = "last_error"
)

const (
	DebugStatRootID   = "root_id"
	DebugStatRootPath = "root_path"
)

type RemoteNameInfo struct {
	PlainName  string `json:"plain_name"`
	RemoteName string `json:"remote_name"`
}

type ForeignEntry struct {
	ID         string `json:"id"`
	ParentID   string `json:"parent_id,omitempty"`
	RemoteName string `json:"remote_name"`
	IsDir      bool   `json:"is_dir"`
	Size       int64  `json:"size,omitempty"`
	Reason     string `json:"reason,omitempty"`
}
