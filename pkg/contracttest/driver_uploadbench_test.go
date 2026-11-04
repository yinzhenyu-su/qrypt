package contracttest

import (
	"context"
	"testing"
)

func TestParseChunkSizeList(t *testing.T) {
	sizes, err := ParseChunkSizeList("4m,8m,16m")
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{4 << 20, 8 << 20, 16 << 20}
	if len(sizes) != len(want) {
		t.Fatalf("sizes = %v, want %v", sizes, want)
	}
	for i := range want {
		if sizes[i] != want[i] {
			t.Fatalf("sizes = %v, want %v", sizes, want)
		}
	}
	if got, err := ParseChunkSizeList(""); err != nil || len(got) != 1 || got[0] != 0 {
		t.Fatalf("empty list = %v err=%v, want [0]", got, err)
	}
	if _, err := ParseChunkSizeList("0"); err == nil {
		t.Fatal("zero chunk size accepted")
	}
}

func TestRunDriverUploadBenchmark(t *testing.T) {
	driver := newCRUDMemoryDriver()
	report := RunDriverUploadBenchmark(context.Background(), "mem", driver, 1<<20, []int{1}, nil, 1, 0)
	if report.Error != "" {
		t.Fatalf("benchmark error: %s", report.Error)
	}
	if report.SupportsChunkSize {
		t.Fatalf("memory driver unexpectedly advertises chunk-size tuning: %+v", report)
	}
	if len(report.Samples) != 1 {
		t.Fatalf("samples = %d, want 1", len(report.Samples))
	}
	sample := report.Samples[0]
	if sample.Bytes != report.FileSize || sample.AggregateBPS <= 0 || sample.Errors != 0 {
		t.Fatalf("sample = %+v, want a clean measurement", sample)
	}
	if report.CleanupError != "" {
		t.Fatalf("cleanup error: %s", report.CleanupError)
	}
}

func TestRunDriverUploadBenchmarkRejectsOversizeSample(t *testing.T) {
	driver := newCRUDMemoryDriver()
	// 32 uploads of 600 MiB exceed the per-sample byte limit, so the run is
	// rejected before any concurrent upload starts.
	report := RunDriverUploadBenchmark(context.Background(), "mem", driver, 600<<20, []int{32}, nil, 1, 0)
	report2 := RunDriverUploadBenchmark(context.Background(), "mem", driver, MaxUploadProbeSize+1, []int{1}, nil, 1, 0)
	if report.Error == "" || report2.Error == "" {
		t.Fatalf("oversize upload benchmark accepted: %q / %q", report.Error, report2.Error)
	}
}
