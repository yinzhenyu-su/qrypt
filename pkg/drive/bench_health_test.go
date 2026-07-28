package drive

import (
	"errors"
	"testing"
)

// BenchmarkHealthRecordSuccess measures the per-op cost of recording a
// successful health result (mutex + append + trim traversal).
func BenchmarkHealthRecordSuccess(b *testing.B) {
	t := NewHealthTracker(DefaultHealthWindow, DefaultMaxEvents)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t.RecordSuccess(HealthOpRead)
	}
}

// BenchmarkHealthRecordError measures the per-op cost of recording an error.
func BenchmarkHealthRecordError(b *testing.B) {
	t := NewHealthTracker(DefaultHealthWindow, DefaultMaxEvents)
	err := errors.New("boom")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t.RecordError(HealthOpRead, err)
	}
}

// BenchmarkHealthStatus measures the cost of a /debug/health aggregation
// (only paid by the debug endpoint, not the hot path).
func BenchmarkHealthStatus(b *testing.B) {
	t := NewHealthTracker(DefaultHealthWindow, DefaultMaxEvents)
	for i := 0; i < 200; i++ {
		t.RecordSuccess(HealthOpRead)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t.Status()
	}
}
