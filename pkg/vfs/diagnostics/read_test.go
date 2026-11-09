package diagnostics

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type recordingReadRuntime struct {
	events  []drive.MetricEvent
	details []drive.MetricEvent
}

func (r *recordingReadRuntime) AppendEvent(event drive.MetricEvent) {
	r.events = append(r.events, event)
}
func (r *recordingReadRuntime) AppendDetailEvent(event drive.MetricEvent) {
	r.details = append(r.details, event)
}
func (r *recordingReadRuntime) History() []drive.MetricEvent { return nil }
func (r *recordingReadRuntime) ResetHistory()                {}

func TestRecordReadDetailPromotesReadPolicyFields(t *testing.T) {
	runtime := &recordingReadRuntime{}
	ctx := drive.WithDebugOperation(context.Background(), drive.DebugOperation{OpID: "op"})
	extra := map[string]any{
		"read_intent":            "bulk",
		"read_streams":           4,
		"prefetch_cancel_reason": "invalidate",
	}
	RecordReadDetail(runtime, ctx, "/file", "id", "prefetch_window", 0, 1, 0, time.Now(), extra, context.Canceled)

	if len(runtime.details) != 1 {
		t.Fatalf("details = %d, want 1", len(runtime.details))
	}
	event := runtime.details[0]
	if event.ReadIntent != "bulk" || event.ReadStreams != 4 || event.PrefetchCancelReason != "invalidate" {
		t.Fatalf("policy fields = %+v", event)
	}
	if event.State != "canceled" || !event.OK {
		t.Fatalf("state = %q ok=%v, want canceled/true", event.State, event.OK)
	}
}

func TestRecordReadSummaryPromotesReadPolicyFields(t *testing.T) {
	runtime := &recordingReadRuntime{}
	extra := map[string]any{
		"read_intent":  "interactive",
		"read_streams": 1,
	}
	RecordRead(runtime, "op", "/file", "id", 0, 1, 1, "remote", 0, 0, 1, time.Now(), extra, nil)

	if len(runtime.events) != 1 {
		t.Fatalf("events = %d, want 1", len(runtime.events))
	}
	event := runtime.events[0]
	if event.ReadIntent != "interactive" || event.ReadStreams != 1 {
		t.Fatalf("policy fields = %+v", event)
	}
	if event.State != "completed" || !event.OK {
		t.Fatalf("state = %q ok=%v, want completed/true", event.State, event.OK)
	}
}

func TestRecordReadDetailForegroundCancelStillFails(t *testing.T) {
	runtime := &recordingReadRuntime{}
	ctx := drive.WithDebugOperation(context.Background(), drive.DebugOperation{OpID: "op"})
	RecordReadDetail(runtime, ctx, "/file", "id", "fetch_window", 0, 1, 0, time.Now(), nil, context.Canceled)

	if len(runtime.details) != 1 {
		t.Fatalf("details = %d, want 1", len(runtime.details))
	}
	if runtime.details[0].State != "failed" || runtime.details[0].OK {
		t.Fatalf("state = %q ok=%v, want failed/false", runtime.details[0].State, runtime.details[0].OK)
	}
}
