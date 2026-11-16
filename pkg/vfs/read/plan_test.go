package read

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"testing"
)

func TestPlanResolvesEveryStageAndIntent(t *testing.T) {
	p := drive.DefaultReadPolicy()
	p.Interactive.Seek = drive.AccessPolicy{Window: drive.WindowPolicy{Mode: drive.WindowFixed, Bytes: ChunkSize}, Prefetch: drive.PrefetchPolicy{Mode: drive.PrefetchDisabled}}
	p.Interactive.Sequential.Prefetch = drive.PrefetchPolicy{Mode: drive.PrefetchFixed, WindowBytes: 8 << 20, Depth: 1}
	for _, stage := range []ReadStage{StageInitial, StageSeek, StageSequential} {
		for _, intent := range []ReadIntent{ReadIntentUnspecified, ReadIntentInteractive, ReadIntentProbe, ReadIntentBulk} {
			plan := (PlanResolver{Profile: p, AdaptiveDepth: 4}).ResolveStage(stage, intent, 2)
			if plan.Intent != intent || plan.Stage != stage {
				t.Fatalf("identity changed: %+v", plan)
			}
			if intent == ReadIntentBulk {
				if plan.MaxStreams != p.Bulk.Streams || plan.CacheWrites {
					t.Fatalf("bulk: %+v", plan)
				}
				continue
			}
			if plan.MaxStreams != 1 {
				t.Fatalf("interactive/probe: %+v", plan)
			}
			if intent == ReadIntentProbe {
				if plan.WindowChunks != 1 || plan.PrefetchDepth != 0 || plan.PrefetchChunks != 0 || plan.CacheWrites {
					t.Fatalf("probe: %+v", plan)
				}
				continue
			}
			if !plan.CacheWrites {
				t.Fatal("lost cache admission")
			}
			if stage == StageSeek && (plan.WindowChunks != 1 || plan.PrefetchDepth != 0) {
				t.Fatalf("seek: %+v", plan)
			}
			if stage == StageSequential && (plan.PrefetchChunks != 16 || plan.PrefetchDepth != 1) {
				t.Fatalf("sequential: %+v", plan)
			}
		}
	}
}
func TestAdaptivePrefetchResolvesAtPlanningBoundary(t *testing.T) {
	r := PlanResolver{Profile: drive.DefaultReadPolicy(), AdaptiveDepth: 3}
	for _, tc := range []struct {
		stage               ReadStage
		caller, size, depth int
	}{{StageInitial, 1, 1, 3}, {StageSequential, 1, 16, 3}, {StageSeek, 2, 2, 1}} {
		p := r.ResolveStage(tc.stage, ReadIntentInteractive, tc.caller)
		if p.PrefetchChunks != tc.size || p.PrefetchDepth != tc.depth {
			t.Fatalf("plan=%+v want %+v", p, tc)
		}
	}
}
func TestReadPlanExtraUsesGrantedStreams(t *testing.T) {
	ctx := WithGrantedStreams(WithReadPlan(context.Background(), ReadPlan{Intent: ReadIntentBulk, MaxStreams: 4}), 2)
	if extra := ReadPlanExtra(ctx, 1); extra["read_streams"] != 2 {
		t.Fatalf("extra=%v", extra)
	}
}
