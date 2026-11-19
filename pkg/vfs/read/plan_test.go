package read

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"testing"
)

func TestPlanResolvesEveryStageAndIntent(t *testing.T) {
	p := drive.DefaultReadPolicy()
	p.Interactive.WindowBytes = 2 << 20
	p.Interactive.PrefetchWindowBytes = 8 << 20
	p.Interactive.PrefetchDepth = 1
	for _, stage := range []ReadStage{StageInitial, StageSeek, StageSequential} {
		for _, intent := range []ReadIntent{ReadIntentUnspecified, ReadIntentInteractive, ReadIntentBulk} {
			plan := (PlanResolver{Profile: p, AdaptiveDepth: 4}).ResolveStage(stage, intent, 2)
			if plan.Intent != intent || plan.Stage != stage {
				t.Fatalf("identity changed: %+v", plan)
			}
			if intent == ReadIntentBulk {
				if plan.MaxStreams != p.Bulk.Streams || plan.cacheWrites() {
					t.Fatalf("bulk: %+v", plan)
				}
				continue
			}
			if plan.MaxStreams != 1 {
				t.Fatalf("interactive/probe: %+v", plan)
			}
			if !plan.cacheWrites() {
				t.Fatal("lost cache admission")
			}
			wantWindow := 2
			if stage == StageSequential {
				wantWindow = byteChunks(2 << 20)
			}
			if plan.WindowChunks != wantWindow {
				t.Fatalf("%v window: %+v", stage, plan)
			}
			if plan.PrefetchChunks != 16 || plan.PrefetchDepth != 1 {
				t.Fatalf("%v lookahead: %+v", stage, plan)
			}
		}
	}
}
func TestAdaptivePrefetchResolvesAtPlanningBoundary(t *testing.T) {
	r := PlanResolver{Profile: drive.DefaultReadPolicy(), AdaptiveDepth: 3}
	for _, tc := range []struct {
		stage               ReadStage
		caller, size, depth int
	}{{StageInitial, 1, 1, 3}, {StageSequential, 1, 1, 3}, {StageSeek, 2, 2, 3}} {
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
