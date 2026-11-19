package contracttest

import (
	"context"
	"strings"
	"testing"
)

func TestTuneRecommendationPicksFastestShapes(t *testing.T) {
	summaries := []ReadTuneSummary{
		{ReadTuneVariant: ReadTuneVariant{Name: "window=caller", Mode: "interactive", WindowBytes: 0}, MedianBPS: 2e6},
		{ReadTuneVariant: ReadTuneVariant{Name: "window=2MiB", Mode: "interactive", WindowBytes: 2 << 20, PrefetchWindowBytes: 2 << 20}, MedianBPS: 3e6},
		{ReadTuneVariant: ReadTuneVariant{Name: "window=4MiB", Mode: "interactive", WindowBytes: 4 << 20, PrefetchWindowBytes: 4 << 20}, MedianBPS: 4e6},
		{ReadTuneVariant: ReadTuneVariant{Name: "depth=2", Mode: "interactive", WindowBytes: 4 << 20, PrefetchWindowBytes: 4 << 20, PrefetchDepth: tuneIntPtr(2)}, MedianBPS: 3.5e6},
		{ReadTuneVariant: ReadTuneVariant{Name: "depth=4", Mode: "interactive", WindowBytes: 4 << 20, PrefetchWindowBytes: 4 << 20, PrefetchDepth: tuneIntPtr(4)}, MedianBPS: 4.5e6},
		{ReadTuneVariant: ReadTuneVariant{Name: "bulk window=16MiB streams=2", Mode: "bulk", WindowBytes: 16 << 20, Streams: 2}, MedianBPS: 3.3e6},
		{ReadTuneVariant: ReadTuneVariant{Name: "bulk window=8MiB streams=1", Mode: "bulk", WindowBytes: 8 << 20, Streams: 1}, MedianBPS: 2.5e6},
	}
	rec := tuneRecommendation(summaries)
	if rec.InteractiveWindowBytes != 4<<20 {
		t.Fatalf("interactive window = %d, want 4 MiB", rec.InteractiveWindowBytes)
	}
	if rec.InteractivePrefetchDepth == nil || *rec.InteractivePrefetchDepth != 4 {
		t.Fatalf("interactive depth = %v, want 4", rec.InteractivePrefetchDepth)
	}
	if rec.BulkWindowBytes != 16<<20 || rec.BulkStreams != 2 {
		t.Fatalf("bulk = %s/%d streams, want 16MiB/2", formatTuneBytes(rec.BulkWindowBytes), rec.BulkStreams)
	}
	if len(rec.Evidence) != 3 {
		t.Fatalf("evidence lines = %d, want 3", len(rec.Evidence))
	}
	config := rec.Config
	for _, want := range []string{"[mounts.read.interactive]", `window = "4MiB"`, "prefetch_depth = 4", "[mounts.read.bulk]", `window = "16MiB"`, "streams = 2"} {
		if !strings.Contains(config, want) {
			t.Fatalf("config snippet missing %q:\n%s", want, config)
		}
	}
}

func TestTuneVariantsCoverInteractiveAndBulk(t *testing.T) {
	windows := tuneWindowVariants()
	if len(windows) < 5 {
		t.Fatalf("window variants = %d, want >=5", len(windows))
	}
	for _, variant := range windows {
		if variant.Name == "" || variant.Mode != "interactive" {
			t.Fatalf("unexpected window variant %+v", variant)
		}
	}
	depths := tuneDepthVariants(2 << 20)
	if len(depths) != 4 {
		t.Fatalf("depth variants = %d, want 4", len(depths))
	}
	for _, variant := range depths {
		if variant.WindowBytes != 2<<20 {
			t.Fatalf("depth variant window = %d, want the winning window 2 MiB", variant.WindowBytes)
		}
	}
	bulk := tuneBulkVariants()
	if len(bulk) != 4 {
		t.Fatalf("bulk variants = %d, want 4", len(bulk))
	}
	if got := rotateVariants(windows, 2)[0].Name; got != windows[2].Name {
		t.Fatalf("rotate by 2 starts at %q, want %q", got, windows[2].Name)
	}
	if got := bestTuneWindow([]ReadTuneSummary{
		{ReadTuneVariant: ReadTuneVariant{Name: "window=caller", Mode: "interactive"}, MedianBPS: 1e6},
		{ReadTuneVariant: ReadTuneVariant{Name: "window=4MiB", Mode: "interactive", WindowBytes: 4 << 20}, MedianBPS: 3e6},
		{ReadTuneVariant: ReadTuneVariant{Name: "depth=4", Mode: "interactive", WindowBytes: 8 << 20}, MedianBPS: 4e6},
	}); got != 4<<20 {
		t.Fatalf("best window = %d, want 4 MiB", got)
	}
}

func TestRunVFSReadTuneRequiresExistingFile(t *testing.T) {
	run := RunVFSReadTune(context.Background(), nil, "cloud", DriverTestRequest{})
	if run.Pass {
		t.Fatal("tune without a path must fail")
	}
	if run.Error == "" || !strings.Contains(run.Error, "--path") {
		t.Fatalf("error = %q, want a --path hint", run.Error)
	}
}

func tuneIntPtr(value int) *int { return &value }
