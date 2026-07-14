package drive

import (
	"errors"
	"testing"
	"time"
)

func TestHealthTrackerInitialState(t *testing.T) {
	tracker := NewHealthTracker(time.Minute, 100)
	status := tracker.Status()
	if !status.OK || status.Level != HealthLevelOK {
		t.Fatalf("initial status = %+v, want ok", status)
	}
	if status.Success != 0 || status.Errors != 0 || len(status.Ops) != 0 {
		t.Fatalf("initial counts = %+v, want empty", status)
	}
}

func TestHealthTrackerRecordsByOperation(t *testing.T) {
	tracker := NewHealthTracker(time.Minute, 100)
	tracker.RecordResult(HealthOpList, nil)
	tracker.RecordResult(HealthOpList, nil)
	tracker.RecordResult(HealthOpUpload, errors.New("upload failed"))

	status := tracker.Status()
	if status.OK || status.Level != HealthLevelDegraded {
		t.Fatalf("status = %+v, want degraded", status)
	}
	if status.Success != 2 || status.Errors != 1 {
		t.Fatalf("counts = success %d errors %d, want 2/1", status.Success, status.Errors)
	}
	if got := status.Ops[HealthOpList]; got.Success != 2 || got.Errors != 0 {
		t.Fatalf("list status = %+v, want 2 successes", got)
	}
	if got := status.Ops[HealthOpUpload]; got.Success != 0 || got.Errors != 1 || got.LastError != "upload failed" {
		t.Fatalf("upload status = %+v, want one upload error", got)
	}
}

func TestHealthTrackerUnhealthyAfterRepeatedOperationFailures(t *testing.T) {
	tracker := NewHealthTracker(time.Minute, 100)
	for range 5 {
		tracker.RecordError(HealthOpRead, errors.New("read failed"))
	}

	status := tracker.Status()
	if status.Level != HealthLevelUnhealthy {
		t.Fatalf("level = %s, want unhealthy: %+v", status.Level, status)
	}
	if got := status.Ops[HealthOpRead]; got.Errors != 5 {
		t.Fatalf("read status = %+v, want 5 errors", got)
	}
}

func TestHealthTrackerSuccessDoesNotHideErrors(t *testing.T) {
	tracker := NewHealthTracker(time.Minute, 100)
	for range 20 {
		tracker.RecordSuccess(HealthOpList)
	}
	tracker.RecordError(HealthOpUpload, errors.New("upload failed"))

	status := tracker.Status()
	if status.Level != HealthLevelDegraded {
		t.Fatalf("level = %s, want degraded despite list successes: %+v", status.Level, status)
	}
}

func TestHealthTrackerExpiry(t *testing.T) {
	tracker := NewHealthTracker(50*time.Millisecond, 100)
	tracker.RecordError(HealthOpRead, errors.New("read failed"))
	if tracker.Status().Level != HealthLevelDegraded {
		t.Fatal("expected degraded after read error")
	}

	time.Sleep(60 * time.Millisecond)

	status := tracker.Status()
	if status.Level != HealthLevelOK || status.Errors != 0 {
		t.Fatalf("expired status = %+v, want ok", status)
	}
}

func TestHealthTrackerMaxEvents(t *testing.T) {
	tracker := NewHealthTracker(time.Hour, 5)
	for range 10 {
		tracker.RecordError(HealthOpRead, errors.New("read failed"))
	}
	if len(tracker.events) > 5 {
		t.Fatalf("max events should be 5, got %d", len(tracker.events))
	}
	if got := tracker.Status().Ops[HealthOpRead]; got.Errors != 5 {
		t.Fatalf("read status = %+v, want last 5 errors", got)
	}
}

func TestHealthStatusFromMetrics(t *testing.T) {
	status := HealthStatusFromMetrics([]MetricEvent{
		{At: time.Now(), Operation: "api_list", OK: true},
		{At: time.Now(), Operation: "api_upload", OK: false, Error: "remote failed"},
	}, time.Minute, 100)

	if status.OK || status.Level != HealthLevelDegraded {
		t.Fatalf("status = %+v, want degraded", status)
	}
	if status.Success != 1 || status.Errors != 1 {
		t.Fatalf("counts = success %d errors %d, want 1/1", status.Success, status.Errors)
	}
	if got := status.Ops["api_upload"]; got.Errors != 1 || got.LastError != "remote failed" {
		t.Fatalf("api_upload status = %+v, want one metric error", got)
	}
}

func TestHealthStatusFromMetricsNormalizesHighCardinalityOperation(t *testing.T) {
	status := HealthStatusFromMetrics([]MetricEvent{
		{At: time.Now(), Operation: "/api/file/list?parent=abc", OK: false, Error: "remote failed"},
	}, time.Minute, 100)

	if got := status.Ops["api_request"]; got.Errors != 1 || got.LastError != "remote failed" {
		t.Fatalf("api_request status = %+v, want normalized metric error", got)
	}
	if _, ok := status.Ops["/api/file/list?parent=abc"]; ok {
		t.Fatalf("high-cardinality operation leaked into health ops: %+v", status.Ops)
	}
}

func TestMergeHealthStatus(t *testing.T) {
	local := HealthTrackerStatus{
		OK:        true,
		Level:     HealthLevelOK,
		CheckedAt: time.Now(),
		Ops:       map[string]HealthOpStatus{HealthOpList: {Success: 1}},
		Success:   1,
	}
	driver := HealthStatusFromMetrics([]MetricEvent{
		{At: time.Now(), Operation: "api_read", OK: false, Error: "read failed"},
	}, time.Minute, 100)

	merged := MergeHealthStatus(local, driver)
	if merged.OK || merged.Level != HealthLevelDegraded {
		t.Fatalf("merged = %+v, want degraded", merged)
	}
	if merged.Success != 1 || merged.Errors != 1 {
		t.Fatalf("merged counts = success %d errors %d, want 1/1", merged.Success, merged.Errors)
	}
}
