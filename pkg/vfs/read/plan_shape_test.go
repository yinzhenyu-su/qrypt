package read

// ReadPlan resolves two shapes only: streaming and bulk. A streaming plan takes
// its window from the caller unless the confirmed sequential stage widens it,
// and its lookahead is one window (W) times a depth (D). There is no mode left
// to infer from a number's provenance: the same effective values behave the
// same whether they came from a default, a driver recommendation, or a mount
// override.

import (
	"context"
	"io"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// recommendingDriver recommends a complete policy, like a driver that
// implements drive.ReadPolicyProvider.
type recommendingDriver struct {
	drive.Driver
	policy drive.ReadPolicy
}

func (recommendingDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.ErrUnexpectedEOF
}
func (recommendingDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityParallelRange}
}
func (d recommendingDriver) ReadPolicy() drive.ReadPolicy { return d.policy }

// defaultPolicyDriver takes the library default, like a driver without a
// policy recommendation.
type defaultPolicyDriver struct{ drive.Driver }

func (defaultPolicyDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.ErrUnexpectedEOF
}
func (defaultPolicyDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityParallelRange}
}

func intPtr(n int) *int { return &n }
func i64Ptr(n int64) *int64 {
	return &n
}

// resolvedPolicy is the effective policy a mount actually executes.
func resolvedPolicy(t *testing.T, d drive.Driver, override *drive.ReadOverride) drive.ResolvedReadPolicy {
	t.Helper()
	resolved, err := drive.ResolveReadPolicy(d, override)
	if err != nil {
		t.Fatalf("resolve read policy: %v", err)
	}
	return resolved
}

// shapePolicy widens the sequential window and asks for a wide lookahead.
func shapePolicy() drive.ReadPolicy {
	p := drive.DefaultReadPolicy()
	p.Interactive.WindowBytes = 4 * ChunkSize
	p.Interactive.PrefetchWindowBytes = 8 << 20
	p.Interactive.PrefetchDepth = 4
	return p
}

// disabledPolicy prefetches nothing.
func disabledPolicy() drive.ReadPolicy {
	p := drive.DefaultReadPolicy()
	p.Interactive.PrefetchDepth = 0
	return p
}

func TestPlanShapesTableDriven(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   drive.ReadPolicy
		stage    ReadStage
		caller   int
		depthCap int
		window   int
		chunks   int
		depth    int
	}{
		{
			name: "default initial single chunk", policy: drive.DefaultReadPolicy(), stage: StageInitial, caller: 1, depthCap: 4,
			window: 1, chunks: 1, depth: 4,
		},
		{
			name: "default sequential single chunk", policy: drive.DefaultReadPolicy(), stage: StageSequential, caller: 1, depthCap: 4,
			window: 1, chunks: 1, depth: 4,
		},
		{
			name: "default seek two chunks", policy: drive.DefaultReadPolicy(), stage: StageSeek, caller: 2, depthCap: 4,
			window: 2, chunks: 2, depth: 4,
		},
		{
			name: "default wide caller window", policy: drive.DefaultReadPolicy(), stage: StageInitial, caller: 4, depthCap: 4,
			window: 4, chunks: 4, depth: 4,
		},
		{
			// Initial access never takes the sequential window.
			name: "shape initial keeps caller window", policy: shapePolicy(), stage: StageInitial, caller: 1, depthCap: 4,
			window: 1, chunks: 16, depth: 4,
		},
		{
			name: "shape sequential widens window", policy: shapePolicy(), stage: StageSequential, caller: 1, depthCap: 4,
			window: 4, chunks: 16, depth: 4,
		},
		{
			name: "depth capped by capacity", policy: shapePolicy(), stage: StageSequential, caller: 1, depthCap: 2,
			window: 4, chunks: 16, depth: 2,
		},
		{
			name: "disabled", policy: disabledPolicy(), stage: StageSequential, caller: 1, depthCap: 4,
			window: 1, chunks: 0, depth: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := resolvedPolicy(t, recommendingDriver{policy: tc.policy}, nil)
			plan := (PlanResolver{Profile: resolved.Effective, AdaptiveDepth: tc.depthCap}).ResolveStage(tc.stage, ReadIntentUnspecified, tc.caller)
			if plan.WindowChunks != tc.window || plan.PrefetchChunks != tc.chunks || plan.PrefetchDepth != tc.depth {
				t.Fatalf("plan = %+v, want window %d chunks %d depth %d", plan, tc.window, tc.chunks, tc.depth)
			}
			if tc.chunks == 0 {
				if plan.prefetchHorizonChunks() != 0 {
					t.Fatalf("disabled plan has horizon %d", plan.prefetchHorizonChunks())
				}
				return
			}
			if got, want := plan.prefetchHorizonChunks(), int64(tc.chunks)*int64(tc.depth); got != want {
				t.Fatalf("horizon = %d chunks, want %d", got, want)
			}
		})
	}
}

// TestPlanIgnoresPolicyProvenance pins the provenance/value separation: the
// same effective values reached through a default, a driver recommendation, or
// a mount override must produce the identical plan. Recommended and effective
// stay distinct inputs.
func TestPlanIgnoresPolicyProvenance(t *testing.T) {
	fromDefault := resolvedPolicy(t, defaultPolicyDriver{}, nil)
	fromDriver := resolvedPolicy(t, recommendingDriver{policy: drive.DefaultReadPolicy()}, nil)
	fromMount := resolvedPolicy(t, defaultPolicyDriver{}, &drive.ReadOverride{
		MaxInFlight:     intPtr(4),
		BulkStreams:     intPtr(4),
		BulkWindowBytes: i64Ptr(8 << 20),
	})
	if fromDefault.Origins["max_in_flight"] != "default" ||
		fromDriver.Origins["max_in_flight"] != "driver" ||
		fromMount.Origins["max_in_flight"] != "mount" {
		t.Fatalf("origins = %v / %v / %v", fromDefault.Origins, fromDriver.Origins, fromMount.Origins)
	}
	if fromMount.Recommended != fromDefault.Recommended {
		t.Fatal("mount override must not rewrite the recommendation")
	}
	for _, stage := range []ReadStage{StageInitial, StageSeek, StageSequential} {
		resolver := func(r drive.ResolvedReadPolicy) ReadPlan {
			return (PlanResolver{Profile: r.Effective, AdaptiveDepth: 4}).ResolveStage(stage, ReadIntentInteractive, 1)
		}
		want := resolver(fromDefault)
		if got := resolver(fromDriver); got != want {
			t.Fatalf("%v: driver-sourced plan %+v differs from default %+v", stage, got, want)
		}
		if got := resolver(fromMount); got != want {
			t.Fatalf("%v: mount-sourced plan %+v differs from default %+v", stage, got, want)
		}
	}
}

// TestPlanStageUpgradeKeepsLookahead pins that a later stage keeps the same
// lookahead shape; the stage may change W but never the mechanism.
func TestPlanStageUpgradeKeepsLookahead(t *testing.T) {
	resolver := PlanResolver{Profile: drive.DefaultReadPolicy(), AdaptiveDepth: 4}
	initial := resolver.ResolveStage(StageInitial, ReadIntentUnspecified, 1)
	sequential := resolver.ResolveStage(StageSequential, ReadIntentUnspecified, 1)
	if initial.PrefetchDepth != sequential.PrefetchDepth || initial.PrefetchChunks != sequential.PrefetchChunks {
		t.Fatalf("lookahead changed across stages: %+v -> %+v", initial, sequential)
	}
	if initial.prefetchHorizonChunks() != sequential.prefetchHorizonChunks() {
		t.Fatalf("stage upgrade changed the fixed horizon: %d -> %d", initial.prefetchHorizonChunks(), sequential.prefetchHorizonChunks())
	}
}

// TestPlanExecutesLiteralNumbers pins that a plan built outside ResolveStage
// executes its numbers literally.
func TestPlanExecutesLiteralNumbers(t *testing.T) {
	plan := ReadPlan{Stage: StageSequential, PrefetchChunks: 2, PrefetchDepth: 3}
	if plan.prefetchHorizonChunks() != 6 {
		t.Fatalf("literal horizon = %d chunks, want 6", plan.prefetchHorizonChunks())
	}
}
