package read

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestPlanResolverUnspecifiedUsesConcreteDefaults(t *testing.T) {
	profile := drive.DefaultReadProfile()
	plan := PlanResolver{Profile: profile}.Resolve(ReadIntentUnspecified, 64<<20, 2)
	if plan.Intent != ReadIntentUnspecified || plan.Streams != 1 || plan.WindowChunks != 2 ||
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
	if large.Intent != ReadIntentBulk || large.Streams != 4 || large.WindowChunks != profile.BulkWindowChunks ||
		large.PrefetchDepth != profile.PrefetchDepth || large.CacheWrites {
		t.Fatalf("large bulk plan = %+v", large)
	}
	small := resolver.Resolve(ReadIntentBulk, 1<<20, 2)
	if small.Streams != 1 {
		t.Fatalf("small bulk streams = %d, want 1", small.Streams)
	}

	interactive := resolver.Resolve(ReadIntentInteractive, 16<<20, 2)
	if interactive.Streams != 1 || interactive.WindowChunks != profile.InteractiveWindowChunks ||
		interactive.PrefetchDepth != profile.PrefetchDepth || !interactive.CacheWrites {
		t.Fatalf("interactive plan = %+v", interactive)
	}

	probe := resolver.Resolve(ReadIntentProbe, 16<<20, 2)
	if probe.Streams != 1 || probe.WindowChunks != profile.ProbeWindowChunks || probe.PrefetchDepth != 0 || probe.CacheWrites {
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
