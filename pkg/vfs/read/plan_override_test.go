package read

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestPlanOverrideAdjustsPlanAndPerFileLimit(t *testing.T) {
	state := NewState(nil)
	reader := &Reader{state: state, profile: func() drive.ReadPolicy { p := drive.DefaultReadPolicy(); p.Bulk.Streams = 1; return p }()}
	if got := reader.streamLimit(); got != 4 {
		t.Fatalf("per-file limit = %d, want 4", got)
	}

	reader.SetPlanOverride(PlanOverride{MaxStreams: 4, WindowChunks: 16})
	if got := reader.streamLimit(); got != 4 {
		t.Fatalf("per-file limit with override = %d, want 4", got)
	}
	plan := ReadPlan{MaxStreams: 1, WindowChunks: 8}
	if !reader.applyPlanOverride(&plan) {
		t.Fatal("override was not applied")
	}
	if plan.MaxStreams != 4 || plan.WindowChunks != 16 {
		t.Fatalf("plan = %+v, want streams 4 window 16", plan)
	}

	reader.ClearPlanOverride()
	if got := reader.streamLimit(); got != 4 {
		t.Fatalf("per-file limit after clear = %d, want 4", got)
	}
	plan = ReadPlan{MaxStreams: 1, WindowChunks: 8}
	if reader.applyPlanOverride(&plan) {
		t.Fatal("cleared override was still applied")
	}
	if plan.MaxStreams != 1 || plan.WindowChunks != 8 {
		t.Fatalf("plan = %+v, want unchanged", plan)
	}
}

func TestStreamMeasurementOverrideRespectsProbeGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		intent ReadIntent
		chunks int
	}{{"ordinary", ReadIntentUnspecified, 4}, {"probe", ReadIntentProbe, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newStagedReader(t, 8<<20)
			r.reader.SetPlanOverride(PlanOverride{WindowChunks: 4, MaxStreams: 1})
			ctx := WithoutReadPrefetch(WithReadIntent(context.Background(), tc.intent))
			body, err := r.reader.ReadStream(ctx, "/file")
			if err != nil {
				t.Fatal(err)
			}
			defer body.Close()
			if _, err := body.Read(make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			calls := r.host.recorded()
			if len(calls) != 1 || calls[0].Size != int64(tc.chunks)*ChunkSize || calls[0].Plan.WindowChunks != tc.chunks {
				t.Fatalf("stream range = %+v", calls)
			}
		})
	}
}

func TestPlanOverrideIgnoresZeroFields(t *testing.T) {
	state := NewState(nil)
	reader := &Reader{state: state, profile: func() drive.ReadPolicy { p := drive.DefaultReadPolicy(); p.Bulk.Streams = 2; return p }()}
	reader.SetPlanOverride(PlanOverride{WindowChunks: 4})
	if got := reader.streamLimit(); got != 4 {
		t.Fatalf("per-file limit = %d, want 4", got)
	}
	plan := ReadPlan{MaxStreams: 2, WindowChunks: 8}
	if !reader.applyPlanOverride(&plan) {
		t.Fatal("window override was not applied")
	}
	if plan.MaxStreams != 2 || plan.WindowChunks != 4 {
		t.Fatalf("plan = %+v, want streams 2 window 4", plan)
	}
}
