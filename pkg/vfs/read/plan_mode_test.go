package read

// ReadPlan must keep the prefetch mode and the stage's allowed ceilings, so a
// later stage never has to guess the rule from a number's provenance. Two
// stages that resolve the same effective values behave identically no matter
// whether the values came from a default, a driver recommendation, or a mount
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

// fixedPolicy is a stage-by-stage fixed plan: no consumption credit applies.
func fixedPolicy() drive.ReadPolicy {
	p := drive.DefaultReadPolicy()
	p.Interactive.Initial = drive.AccessPolicy{
		Window:   drive.WindowPolicy{Mode: drive.WindowFixed, Bytes: 4 * ChunkSize},
		Prefetch: drive.PrefetchPolicy{Mode: drive.PrefetchFixed, WindowBytes: 4 * ChunkSize, Depth: 2},
	}
	p.Interactive.Seek = p.Interactive.Initial
	p.Interactive.Sequential = drive.AccessPolicy{
		Window:   drive.WindowPolicy{Mode: drive.WindowFixed, Bytes: 4 * ChunkSize},
		Prefetch: drive.PrefetchPolicy{Mode: drive.PrefetchFixed, WindowBytes: 8 << 20, Depth: 4},
	}
	return p
}

// disabledPolicy prefetches nothing anywhere.
func disabledPolicy() drive.ReadPolicy {
	p := drive.DefaultReadPolicy()
	off := drive.AccessPolicy{
		Window:   drive.WindowPolicy{Mode: drive.WindowCaller},
		Prefetch: drive.PrefetchPolicy{Mode: drive.PrefetchDisabled},
	}
	p.Interactive.Initial, p.Interactive.Seek, p.Interactive.Sequential = off, off, off
	return p
}

func TestPlanModesTableDriven(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy drive.ReadPolicy
		stage  ReadStage
		caller int
		mode   drive.PrefetchMode
		chunks int
		depth  int
	}{
		{
			name: "adaptive initial single chunk", policy: drive.DefaultReadPolicy(), stage: StageInitial, caller: 1,
			mode: drive.PrefetchAdaptive, chunks: 1, depth: 4,
		},
		{
			// The adaptive rule executes one foreground window per active
			// slot; a sequential FUSE read keeps the same single-chunk window.
			name: "adaptive sequential single chunk", policy: drive.DefaultReadPolicy(), stage: StageSequential, caller: 1,
			mode: drive.PrefetchAdaptive, chunks: 1, depth: 4,
		},
		{
			name: "adaptive seek two chunks", policy: drive.DefaultReadPolicy(), stage: StageSeek, caller: 2,
			mode: drive.PrefetchAdaptive, chunks: 2, depth: 4,
		},
		{
			name: "adaptive wide caller window", policy: drive.DefaultReadPolicy(), stage: StageInitial, caller: 4,
			mode: drive.PrefetchAdaptive, chunks: 4, depth: 4,
		},
		{
			name: "fixed initial", policy: fixedPolicy(), stage: StageInitial, caller: 1,
			mode: drive.PrefetchFixed, chunks: 4, depth: 2,
		},
		{
			name: "fixed sequential", policy: fixedPolicy(), stage: StageSequential, caller: 1,
			mode: drive.PrefetchFixed, chunks: 16, depth: 4,
		},
		{
			name: "disabled", policy: disabledPolicy(), stage: StageSequential, caller: 1,
			mode: drive.PrefetchDisabled, chunks: 0, depth: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := resolvedPolicy(t, recommendingDriver{policy: tc.policy}, nil)
			plan := (PlanResolver{Profile: resolved.Effective, AdaptiveDepth: 4}).ResolveStage(tc.stage, ReadIntentUnspecified, tc.caller)
			if plan.PrefetchMode != tc.mode {
				t.Fatalf("mode = %q, want %q", plan.PrefetchMode, tc.mode)
			}
			if plan.PrefetchChunks != tc.chunks || plan.PrefetchDepth != tc.depth {
				t.Fatalf("window/depth = %d x %d, want %d x %d", plan.PrefetchChunks, plan.PrefetchDepth, tc.chunks, tc.depth)
			}
			if got, want := plan.prefetchHorizonChunks(), int64(max(1, tc.chunks)*max(1, tc.depth)); got != want && tc.chunks > 0 {
				t.Fatalf("horizon = %d chunks, want %d", got, want)
			}
			if tc.chunks == 0 && plan.prefetchHorizonChunks() != 0 {
				t.Fatalf("disabled plan has horizon %d", plan.prefetchHorizonChunks())
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

// TestPlanStageUpgradeKeepsMode pins that a later stage keeps the same prefetch
// rule; the stage may change W×D but never the mechanism.
func TestPlanStageUpgradeKeepsMode(t *testing.T) {
	resolver := PlanResolver{Profile: drive.DefaultReadPolicy(), AdaptiveDepth: 4}
	initial := resolver.ResolveStage(StageInitial, ReadIntentUnspecified, 1)
	sequential := resolver.ResolveStage(StageSequential, ReadIntentUnspecified, 1)
	if initial.PrefetchMode != drive.PrefetchAdaptive || sequential.PrefetchMode != drive.PrefetchAdaptive {
		t.Fatalf("mode changed across stages: %q -> %q", initial.PrefetchMode, sequential.PrefetchMode)
	}
	if initial.prefetchHorizonChunks() != sequential.prefetchHorizonChunks() {
		t.Fatalf("stage upgrade changed the fixed horizon: %d -> %d", initial.prefetchHorizonChunks(), sequential.prefetchHorizonChunks())
	}
}

// TestPlanWithoutModeKeepsLiteralNumbers pins the non-adaptive fallback: a plan
// built outside ResolveStage (bulk bursts, ReadStream, probe overrides) has no
// adaptive mode and executes its numbers literally.
func TestPlanWithoutModeKeepsLiteralNumbers(t *testing.T) {
	plan := ReadPlan{Stage: StageSequential, PrefetchChunks: 2, PrefetchDepth: 3}
	if plan.prefetchHorizonChunks() != 6 {
		t.Fatalf("literal horizon = %d chunks, want 6", plan.prefetchHorizonChunks())
	}
}
