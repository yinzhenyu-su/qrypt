package drive

import (
	"testing"
	"time"
)

func TestNormalizeMetricEventsNormalizesHighCardinalityOperation(t *testing.T) {
	metrics := NormalizeMetricEvents("driver", []MetricEvent{{
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
	if metrics[0].Kind != "driver" {
		t.Fatalf("kind = %q, want driver", metrics[0].Kind)
	}
	if metrics[0].Extra["raw_operation"] != "/api/file/list?parent=abc" {
		t.Fatalf("raw_operation = %+v, want original operation", metrics[0].Extra)
	}
}

func TestNormalizeMetricEventsPreservesLowCardinalityOperation(t *testing.T) {
	metrics := NormalizeMetricEvents("driver", []MetricEvent{{
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

func TestNormalizeMetricEventsDoesNotInferOperationFromDebugLabels(t *testing.T) {
	metrics := NormalizeMetricEvents("driver", []MetricEvent{{
		At:   time.Now(),
		Step: "create uniquely named fixture",
		Name: "crud creates directory",
		Kind: "driver",
	}})
	if len(metrics) != 1 {
		t.Fatalf("metrics len = %d, want 1", len(metrics))
	}
	if metrics[0].Operation != "" {
		t.Fatalf("operation = %q, want empty", metrics[0].Operation)
	}
}

func TestNormalizeMetricEventsDoesNotDuplicateTopLevelFieldsInExtra(t *testing.T) {
	metrics := NormalizeMetricEvents("driver", []MetricEvent{{
		At:        time.Now(),
		Operation: "api_request",
		Method:    "GET",
		URL:       "https://example.invalid/api",
		Status:    200,
		Retry:     true,
		Request:   map[string]any{"headers": []string{"Accept"}},
		Response:  map[string]any{"bytes": 2},
	}})
	if len(metrics) != 1 {
		t.Fatalf("metrics len = %d, want 1", len(metrics))
	}
	if metrics[0].Extra != nil {
		t.Fatalf("extra = %#v, want nil", metrics[0].Extra)
	}
}

func TestNormalizeMetricEventsMasksSensitiveURL(t *testing.T) {
	metrics := NormalizeMetricEvents("driver", []MetricEvent{{
		At:              time.Now(),
		Operation:       "download",
		Method:          "GET",
		URL:             "https://download.example/path/secret-token",
		SensitiveMasked: true,
	}})
	if len(metrics) != 1 {
		t.Fatalf("metrics len = %d, want 1", len(metrics))
	}
	if metrics[0].URL != "" {
		t.Fatalf("url = %q, want masked", metrics[0].URL)
	}
	if got := metrics[0].Request["url_host"]; got != "download.example" {
		t.Fatalf("url_host = %v, want download.example", got)
	}
}

func TestNormalizeMetricEventsKeepsNonSensitiveURL(t *testing.T) {
	metrics := NormalizeMetricEvents("driver", []MetricEvent{{
		At:        time.Now(),
		Operation: "api_request",
		Method:    "GET",
		URL:       "https://example.invalid/api",
	}})
	if len(metrics) != 1 {
		t.Fatalf("metrics len = %d, want 1", len(metrics))
	}
	if metrics[0].URL != "https://example.invalid/api" {
		t.Fatalf("url = %q, want original", metrics[0].URL)
	}
}
