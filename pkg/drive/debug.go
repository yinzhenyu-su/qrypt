package drive

import (
	"context"
	"net/url"
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

type MetricEvent struct {
	At              time.Time      `json:"at"`
	OpID            string         `json:"op_id,omitempty"`
	ParentOpID      string         `json:"parent_op_id,omitempty"`
	Step            string         `json:"step,omitempty"`
	Name            string         `json:"name,omitempty"`
	Kind            string         `json:"kind,omitempty"`
	Layer           string         `json:"layer,omitempty"`
	Mount           string         `json:"mount,omitempty"`
	Driver          string         `json:"driver,omitempty"`
	Operation       string         `json:"operation,omitempty"`
	Phase           string         `json:"phase,omitempty"`
	State           string         `json:"state,omitempty"`
	OK              bool           `json:"ok"`
	Method          string         `json:"method,omitempty"`
	URL             string         `json:"url,omitempty"`
	Status          int            `json:"status,omitempty"`
	Path            string         `json:"path,omitempty"`
	RemoteID        string         `json:"remote_id,omitempty"`
	Offset          int64          `json:"offset,omitempty"`
	Requested       int64          `json:"requested_bytes,omitempty"`
	Duration        string         `json:"duration,omitempty"`
	DurationMS      int64          `json:"duration_ms,omitempty"`
	Bytes           int64          `json:"bytes,omitempty"`
	Throughput      int64          `json:"throughput,omitempty"`
	Attempt         int            `json:"attempt,omitempty"`
	Attempts        int            `json:"attempts,omitempty"`
	RetryCount      int            `json:"retry_count,omitempty"`
	Retry           bool           `json:"retry,omitempty"`
	CacheHits       int64          `json:"cache_hits,omitempty"`
	CacheMisses     int64          `json:"cache_misses,omitempty"`
	Chunks          int64          `json:"chunks,omitempty"`
	StartedAt       time.Time      `json:"started_at,omitempty"`
	FinishedAt      time.Time      `json:"finished_at,omitempty"`
	Request         map[string]any `json:"request,omitempty"`
	Response        map[string]any `json:"response,omitempty"`
	Error           string         `json:"error,omitempty"`
	ErrorCategory   string         `json:"error_category,omitempty"`
	SensitiveMasked bool           `json:"sensitive_masked,omitempty"`
	Extra           map[string]any `json:"extra,omitempty"`
}

func NormalizeMetricEvents(driver string, raw []MetricEvent) []MetricEvent {
	if len(raw) == 0 {
		return nil
	}
	events := make([]MetricEvent, 0, len(raw))
	for _, event := range raw {
		duration, _ := time.ParseDuration(event.Duration)
		request := cloneMetricMap(event.Request)
		metricURL := event.URL
		if event.SensitiveMasked {
			metricURL = ""
			if host := metricURLHost(event.URL); host != "" {
				if request == nil {
					request = map[string]any{}
				}
				if _, exists := request["url_host"]; !exists {
					request["url_host"] = host
				}
			}
		}
		metric := MetricEvent{
			At:              event.At,
			OpID:            event.OpID,
			ParentOpID:      event.ParentOpID,
			Step:            event.Step,
			Name:            event.Name,
			Kind:            firstNonEmpty(event.Kind, "driver"),
			Layer:           event.Layer,
			Mount:           event.Mount,
			Driver:          driver,
			Operation:       metricOperation(event),
			Phase:           firstNonEmpty(event.Method, event.Phase),
			State:           event.State,
			OK:              event.Error == "",
			Method:          event.Method,
			URL:             metricURL,
			Status:          event.Status,
			Path:            event.Path,
			RemoteID:        event.RemoteID,
			Offset:          event.Offset,
			Requested:       event.Requested,
			Duration:        event.Duration,
			DurationMS:      durationMillis(duration),
			Bytes:           event.Bytes,
			Throughput:      event.Throughput,
			RetryCount:      event.RetryCount,
			Error:           event.Error,
			ErrorCategory:   ErrorCategoryMessage(event.Error),
			Retry:           event.Retry,
			CacheHits:       event.CacheHits,
			CacheMisses:     event.CacheMisses,
			Chunks:          event.Chunks,
			StartedAt:       event.StartedAt,
			FinishedAt:      event.FinishedAt,
			Request:         request,
			Response:        cloneMetricMap(event.Response),
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

func metricOperation(event MetricEvent) string {
	op := event.Operation
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

func traceMetricExtra(event MetricEvent) map[string]any {
	extra := map[string]any{}
	for key, value := range event.Extra {
		extra[key] = value
	}
	if event.Operation != "" && isHighCardinalityOperation(event.Operation) {
		extra["raw_operation"] = event.Operation
	}
	if len(extra) == 0 {
		return nil
	}
	return extra
}

func cloneMetricMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func metricURLHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
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
