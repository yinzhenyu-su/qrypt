package debug

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/contracttest"
)

func TestParseReadShapeSpec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		value     string
		wantLabel string
		wantErr   bool
	}{
		{name: "control", value: "control", wantLabel: "control"},
		{name: "empty", value: "", wantLabel: "control"},
		{name: "window", value: "window=2MiB", wantLabel: "window=2097152"},
		{name: "prefetch", value: "prefetch-window=1MiB,prefetch-depth=2", wantLabel: "prefetch-window=1048576,prefetch-depth=2"},
		{name: "parallel", value: "streams=2,min-parallel=8MiB,part-size=4MiB", wantLabel: "part-size=4194304,min-parallel=8388608,streams=2"},
		{name: "part auto", value: "part-size=auto", wantLabel: "part-size=auto"},
		{name: "no prefetch", value: "no-prefetch", wantLabel: "no-prefetch=true"},
		{name: "unknown field", value: "chunk=2", wantErr: true},
		{name: "bad depth", value: "prefetch-depth=5", wantErr: true},
		{name: "bad size", value: "window=0", wantErr: true},
		{name: "bad shape", value: "window", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			spec, err := parseReadShapeSpec(test.value)
			if test.wantErr {
				if err == nil {
					t.Fatalf("parseReadShapeSpec(%q) succeeded, want error", test.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseReadShapeSpec(%q): %v", test.value, err)
			}
			if got := spec.String(); got != test.wantLabel {
				t.Fatalf("parseReadShapeSpec(%q).String() = %q, want %q", test.value, got, test.wantLabel)
			}
		})
	}
}

func TestReadShapeSpecEquivalent(t *testing.T) {
	t.Parallel()
	a, err := parseReadShapeSpec("window=4MiB,prefetch-depth=3")
	if err != nil {
		t.Fatalf("parse a: %v", err)
	}
	b, err := parseReadShapeSpec("window=4MiB,prefetch-depth=3")
	if err != nil {
		t.Fatalf("parse b: %v", err)
	}
	if !a.equivalent(b) {
		t.Fatal("identical specs must be equivalent despite separate pointers")
	}
	c, err := parseReadShapeSpec("window=4MiB")
	if err != nil {
		t.Fatalf("parse c: %v", err)
	}
	if a.equivalent(c) {
		t.Fatal("specs with different prefetch depth must not be equivalent")
	}
}

func TestReadShapeSpecAppliesOverrides(t *testing.T) {
	t.Parallel()
	spec, err := parseReadShapeSpec("window=2MiB,prefetch-window=2MiB,prefetch-depth=4,streams=2,part-size=auto,min-parallel=8MiB")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	req := contracttest.DriverTestRequest{}
	spec.apply(&req)
	if req.WindowBytes != 2<<20 || req.PrefetchWindowBytes != 2<<20 || req.Streams != 2 {
		t.Fatalf("window/prefetch/streams = %d/%d/%d", req.WindowBytes, req.PrefetchWindowBytes, req.Streams)
	}
	if req.PrefetchDepth == nil || *req.PrefetchDepth != 4 {
		t.Fatalf("prefetch depth = %v, want 4", req.PrefetchDepth)
	}
	if req.PartBytes == nil || *req.PartBytes != 0 {
		t.Fatalf("part bytes = %v, want auto (0)", req.PartBytes)
	}
	if req.MinParallelBytes == nil || *req.MinParallelBytes != 8<<20 {
		t.Fatalf("min parallel = %v, want 8 MiB", req.MinParallelBytes)
	}
}

func TestSummarizeABRead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		a, b       []int64
		wantRatio  float64
		wantWinner string
	}{
		{name: "a faster", a: []int64{4 << 20, 5 << 20, 3 << 20}, b: []int64{2 << 20, 2 << 20, 1 << 20}, wantRatio: 2, wantWinner: "a"},
		{name: "b faster", a: []int64{1 << 20, 1 << 20}, b: []int64{2 << 20, 4 << 20}, wantRatio: 1.0 / 3, wantWinner: "b"},
		{name: "tie", a: []int64{2 << 20, 2 << 20}, b: []int64{2 << 20, 2 << 20}, wantRatio: 1, wantWinner: "tie"},
		{name: "empty", a: nil, b: nil, wantWinner: "tie"},
		{name: "even median", a: []int64{2, 4, 4, 6}, b: []int64{2, 2, 2, 2}, wantRatio: 2, wantWinner: "a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := summarizeABRead(test.a, test.b)
			if got.Winner != test.wantWinner {
				t.Fatalf("winner = %q, want %q (summary %+v)", got.Winner, test.wantWinner, got)
			}
			if test.wantRatio > 0 && (got.Ratio < test.wantRatio-0.001 || got.Ratio > test.wantRatio+0.001) {
				t.Fatalf("ratio = %v, want %v", got.Ratio, test.wantRatio)
			}
		})
	}
}

func TestMedianBPS(t *testing.T) {
	t.Parallel()
	if got := medianBPS(nil); got != 0 {
		t.Fatalf("median(nil) = %d", got)
	}
	if got := medianBPS([]int64{5, 1, 3}); got != 3 {
		t.Fatalf("median([5 1 3]) = %d", got)
	}
	if got := medianBPS([]int64{4, 1, 3, 2}); got != 2 {
		t.Fatalf("median([4 1 3 2]) = %d", got)
	}
}
