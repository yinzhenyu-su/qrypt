package drive

import (
	"testing"
	"time"
)

func TestMetricsFromTraceNormalizesHighCardinalityOperation(t *testing.T) {
	metrics := MetricsFromTrace("driver", []DebugTraceEvent{{
		At:        time.Now(),
		Operation: "/api/file/list?parent=abc",
		Method:    "POST",
		URL:       "https://example.invalid/api/file/list?parent=abc",
		Duration:  "2ms",
	}})
	if len(metrics) != 1 {
		t.Fatalf("metrics len = %d, want 1", len(metrics))
	}
	if metrics[0].Operation != "api_request" {
		t.Fatalf("operation = %q, want api_request", metrics[0].Operation)
	}
	if metrics[0].Extra["raw_operation"] != "/api/file/list?parent=abc" {
		t.Fatalf("raw_operation = %+v, want original operation", metrics[0].Extra)
	}
}

func TestMetricsFromTracePreservesLowCardinalityOperation(t *testing.T) {
	metrics := MetricsFromTrace("driver", []DebugTraceEvent{{
		At:        time.Now(),
		Operation: "upload_part",
		Method:    "PUT",
		Duration:  "2ms",
	}})
	if len(metrics) != 1 {
		t.Fatalf("metrics len = %d, want 1", len(metrics))
	}
	if metrics[0].Operation != "upload_part" {
		t.Fatalf("operation = %q, want upload_part", metrics[0].Operation)
	}
}
