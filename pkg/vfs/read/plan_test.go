package read

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestPlanResolverUnspecifiedUsesConcreteDefaults(t *testing.T) {
	profile := drive.DefaultReadProfile()
	plan := PlanResolver{Profile: profile}.Resolve(ReadIntentUnspecified, 64<<20, 2)
	if plan.Intent != ReadIntentUnspecified || plan.MaxStreams != 1 || plan.WindowChunks != 2 ||
		plan.PrefetchDepth != profile.PrefetchDepth || !plan.CacheWrites {
		t.Fatalf("unspecified plan = %+v, want one stream, caller window, prefetch, cache", plan)
	}
	if got := (PlanResolver{Profile: profile}).Resolve(ReadIntentUnspecified, 64<<20, 0).WindowChunks; got != 1 {
		t.Fatalf("zero default window = %d, want 1", got)
	}
}

func TestPlanResolverExplicitIntents(t *testing.T) {
	profile := drive.DefaultReadProfile()
	profile.BulkStreams = 4
	profile.ParallelThreshold = 8 << 20
	resolver := PlanResolver{Profile: profile}

	large := resolver.Resolve(ReadIntentBulk, 16<<20, 2)
	if large.Intent != ReadIntentBulk || large.MaxStreams != 4 || large.WindowChunks != profile.BulkWindowChunks ||
		large.PrefetchDepth != profile.PrefetchDepth || large.CacheWrites {
		t.Fatalf("large bulk plan = %+v", large)
	}
	small := resolver.Resolve(ReadIntentBulk, 1<<20, 2)
	if small.MaxStreams != 1 {
		t.Fatalf("small bulk streams = %d, want 1", small.MaxStreams)
	}

	interactive := resolver.Resolve(ReadIntentInteractive, 16<<20, 2)
	if interactive.MaxStreams != 1 || interactive.WindowChunks != profile.InteractiveWindowChunks ||
		interactive.PrefetchDepth != profile.PrefetchDepth || !interactive.CacheWrites {
		t.Fatalf("interactive plan = %+v", interactive)
	}

	probe := resolver.Resolve(ReadIntentProbe, 16<<20, 2)
	if probe.MaxStreams != 1 || probe.WindowChunks != profile.ProbeWindowChunks || probe.PrefetchDepth != 0 || probe.CacheWrites {
		t.Fatalf("probe plan = %+v", probe)
	}
}

func TestPlanResolverFallsBackWhenProfileWindowIsZero(t *testing.T) {
	profile := drive.DefaultReadProfile()
	profile.BulkWindowChunks = 0
	profile.InteractiveWindowChunks = 0
	profile.ProbeWindowChunks = 0
	resolver := PlanResolver{Profile: profile}

	if got := resolver.Resolve(ReadIntentBulk, 16<<20, 3).WindowChunks; got != 3 {
		t.Fatalf("bulk window = %d, want caller default 3", got)
	}
	if got := resolver.Resolve(ReadIntentInteractive, 16<<20, 3).WindowChunks; got != 3 {
		t.Fatalf("interactive window = %d, want caller default 3", got)
	}
	if got := resolver.Resolve(ReadIntentProbe, 16<<20, 3).WindowChunks; got != 3 {
		t.Fatalf("probe window = %d, want caller default 3", got)
	}
}

func TestReadPlanExtraUsesGrantedStreams(t *testing.T) {
	ctx := WithReadPlan(context.Background(), ReadPlan{Intent: ReadIntentBulk, MaxStreams: 4})
	ctx = WithGrantedStreams(ctx, 2)
	extra := ReadPlanExtra(ctx, 1)
	if got := extra["read_streams"]; got != 2 {
		t.Fatalf("read_streams = %v, want the granted 2", got)
	}
}

func TestPlanResolverProfileWindowOnlyChangesBulk(t *testing.T) {
	profile := drive.DefaultReadProfile()
	profile.BulkStreams = 4
	profile.BulkWindowChunks = 16
	resolver := PlanResolver{Profile: profile}

	bulk := resolver.Resolve(ReadIntentBulk, 32<<20, 2)
	if bulk.WindowChunks != 16 || bulk.MaxStreams != 4 {
		t.Fatalf("bulk plan = %+v, want 16 chunks / 4 streams", bulk)
	}
	interactive := resolver.Resolve(ReadIntentInteractive, 32<<20, 2)
	if interactive.WindowChunks != profile.InteractiveWindowChunks || interactive.MaxStreams != 1 {
		t.Fatalf("interactive plan = %+v, want its own window and one stream", interactive)
	}
	probe := resolver.Resolve(ReadIntentProbe, 32<<20, 2)
	if probe.WindowChunks != profile.ProbeWindowChunks || probe.MaxStreams != 1 {
		t.Fatalf("probe plan = %+v, want its own window and one stream", probe)
	}
}
