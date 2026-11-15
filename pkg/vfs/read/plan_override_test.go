package read

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestPlanOverrideAdjustsPlanAndPerFileLimit(t *testing.T) {
	state := NewState(nil)
	reader := &Reader{state: state, profile: drive.ReadProfile{BulkStreams: 1}}
	if got := reader.perFileLimit(); got != 1 {
		t.Fatalf("per-file limit = %d, want 1", got)
	}

	reader.SetPlanOverride(PlanOverride{MaxStreams: 4, WindowChunks: 16})
	if got := reader.perFileLimit(); got != 4 {
		t.Fatalf("per-file limit with override = %d, want 4", got)
	}
	plan := ReadPlan{MaxStreams: 1, WindowChunks: 8}
	windowChunks := 8
	if !reader.applyPlanOverride(&plan, &windowChunks) {
		t.Fatal("override was not applied")
	}
	if plan.MaxStreams != 4 || plan.WindowChunks != 16 || windowChunks != 16 {
		t.Fatalf("plan = %+v window = %d, want streams 4 window 16", plan, windowChunks)
	}

	reader.ClearPlanOverride()
	if got := reader.perFileLimit(); got != 1 {
		t.Fatalf("per-file limit after clear = %d, want 1", got)
	}
	plan = ReadPlan{MaxStreams: 1, WindowChunks: 8}
	windowChunks = 8
	if reader.applyPlanOverride(&plan, &windowChunks) {
		t.Fatal("cleared override was still applied")
	}
	if plan.MaxStreams != 1 || windowChunks != 8 {
		t.Fatalf("plan = %+v window = %d, want unchanged", plan, windowChunks)
	}
}

func TestPlanOverrideIgnoresZeroFields(t *testing.T) {
	state := NewState(nil)
	reader := &Reader{state: state, profile: drive.ReadProfile{BulkStreams: 2}}
	reader.SetPlanOverride(PlanOverride{WindowChunks: 4})
	if got := reader.perFileLimit(); got != 2 {
		t.Fatalf("per-file limit = %d, want 2", got)
	}
	plan := ReadPlan{MaxStreams: 2, WindowChunks: 8}
	windowChunks := 8
	if !reader.applyPlanOverride(&plan, &windowChunks) {
		t.Fatal("window override was not applied")
	}
	if plan.MaxStreams != 2 || windowChunks != 4 {
		t.Fatalf("plan = %+v window = %d, want streams 2 window 4", plan, windowChunks)
	}
}
