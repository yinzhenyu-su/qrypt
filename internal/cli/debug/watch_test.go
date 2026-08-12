package debug

import (
	"context"
	"testing"
	"time"
)

func TestWatchDebugAIStreamsSamples(t *testing.T) {
	var samples []DebugAIWatchSample
	report := WatchDebugAI(context.Background(), "", 250*time.Millisecond, 100*time.Millisecond, 5, nil, false, func(sample DebugAIWatchSample) {
		samples = append(samples, sample)
	})
	if len(samples) == 0 {
		t.Fatal("jsonl streaming produced no samples")
	}
	if len(report.Samples) != len(samples) {
		t.Fatalf("report has %d samples, callback received %d", len(report.Samples), len(samples))
	}
}

func TestWatchDebugAINilCallbackStillCollects(t *testing.T) {
	report := WatchDebugAI(context.Background(), "", 250*time.Millisecond, 100*time.Millisecond, 5, nil, false, nil)
	if len(report.Samples) == 0 {
		t.Fatal("watch with nil callback produced no samples")
	}
}
