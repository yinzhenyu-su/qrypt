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
		name       string
		profile    drive.ReadPolicy
		stage      ReadStage
		caller     int
		mode       drive.PrefetchMode
		chunks     int
		depth      int
		credit     bool
		windowCap  int64
		horizonCap int64
		initial    int64
	}{
		{
			name: "adaptive initial single chunk", profile: drive.DefaultReadPolicy(), stage: StageInitial, caller: 1,
			mode: drive.PrefetchAdaptive, chunks: 1, depth: 4, credit: true,
			windowCap: ChunkSize, horizonCap: 4 * ChunkSize, initial: ChunkSize,
		},
		{
			name: "adaptive sequential single chunk", profile: drive.DefaultReadPolicy(), stage: StageSequential, caller: 1,
			mode: drive.PrefetchAdaptive, chunks: SequentialPrefetchChunks, depth: 4, credit: true,
			windowCap: int64(SequentialPrefetchChunks) * ChunkSize, horizonCap: int64(SequentialPrefetchChunks) * ChunkSize * 4, initial: 1 << 20,
		},
		{
			name: "adaptive seek single chunk", profile: drive.DefaultReadPolicy(), stage: StageSeek, caller: 2,
			mode: drive.PrefetchAdaptive, chunks: 2, depth: 1, credit: true,
			windowCap: 2 * ChunkSize, horizonCap: 2 * ChunkSize, initial: 1 << 20,
		},
		{
			// A wide caller window still starts from the 1 MiB seed, capped by
			// the single window the stage allows.
			name: "adaptive wide caller window", profile: drive.DefaultReadPolicy(), stage: StageInitial, caller: 4,
			mode: drive.PrefetchAdaptive, chunks: 4, depth: 1, credit: true,
			windowCap: 4 * ChunkSize, horizonCap: 4 * ChunkSize, initial: 1 << 20,
		},
		{
			name: "fixed initial", profile: fixedPolicy(), stage: StageInitial, caller: 1,
			mode: drive.PrefetchFixed, chunks: 4, depth: 2, credit: false,
			windowCap: 4 * ChunkSize, horizonCap: 8 * ChunkSize, initial: 8 * ChunkSize,
		},
		{
			name: "fixed sequential", profile: fixedPolicy(), stage: StageSequential, caller: 1,
			mode: drive.PrefetchFixed, chunks: 16, depth: 4, credit: false,
			windowCap: 8 << 20, horizonCap: 32 << 20, initial: 32 << 20,
		},
		{
			name: "disabled", profile: disabledPolicy(), stage: StageSequential, caller: 1,
			mode: drive.PrefetchDisabled, chunks: 0, depth: 0, credit: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := resolvedPolicy(t, recommendingDriver{policy: tc.profile}, nil)
			plan := (PlanResolver{Profile: resolved.Effective, AdaptiveDepth: 4}).ResolveStage(tc.stage, ReadIntentUnspecified, tc.caller)
			if plan.PrefetchMode != tc.mode {
				t.Fatalf("mode = %q, want %q", plan.PrefetchMode, tc.mode)
			}
			if plan.PrefetchChunks != tc.chunks || plan.PrefetchDepth != tc.depth {
				t.Fatalf("ceilings = %d x %d, want %d x %d", plan.PrefetchChunks, plan.PrefetchDepth, tc.chunks, tc.depth)
			}
			if got := plan.usesConsumptionCredit(); got != tc.credit {
				t.Fatalf("consumption credit = %v, want %v", got, tc.credit)
			}
			if tc.chunks == 0 {
				return
			}
			if got := plan.windowCapBytes(); got != tc.windowCap {
				t.Fatalf("Wcap = %d, want %d", got, tc.windowCap)
			}
			if got := plan.horizonCapBytes(); got != tc.horizonCap {
				t.Fatalf("Hcap = %d, want %d", got, tc.horizonCap)
			}
			if got := plan.initialHorizonBytes(); got != tc.initial {
				t.Fatalf("initial forward budget = %d, want %d", got, tc.initial)
			}
			if plan.initialHorizonBytes() > plan.horizonCapBytes() {
				t.Fatalf("initial budget %d exceeds Hcap %d", plan.initialHorizonBytes(), plan.horizonCapBytes())
			}
			if tc.credit && plan.initialHorizonBytes() > plan.windowCapBytes() {
				t.Fatalf("adaptive seed %d exceeds Wcap %d", plan.initialHorizonBytes(), plan.windowCapBytes())
			}
			if plan.initialHorizonBytes()%ChunkSize != 0 {
				t.Fatalf("initial budget %d is not chunk aligned", plan.initialHorizonBytes())
			}
		})
	}
}

// TestPlanIgnoresPolicyProvenance pins the provenance/value separation: the
// same effective values reached through a default, a driver recommendation, or
// a mount override must produce the identical plan. Recommended and effective
// stay distinct inputs, and the session's dynamic budget stays a third layer
// resolved per read.
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

// TestPlanStageUpgradeKeepsModeAndRaisesCaps pins the boundary between the
// stage (a larger allowed ceiling) and the session (the dynamic budget): the
// upgrade must not silently become a different rule, and it must not change
// the numbers already in flight on its own.
func TestPlanStageUpgradeKeepsModeAndRaisesCaps(t *testing.T) {
	resolver := PlanResolver{Profile: drive.DefaultReadPolicy(), AdaptiveDepth: 4}
	initial := resolver.ResolveStage(StageInitial, ReadIntentUnspecified, 1)
	sequential := resolver.ResolveStage(StageSequential, ReadIntentUnspecified, 1)
	if initial.PrefetchMode != drive.PrefetchAdaptive || sequential.PrefetchMode != drive.PrefetchAdaptive {
		t.Fatalf("mode changed across stages: %q -> %q", initial.PrefetchMode, sequential.PrefetchMode)
	}
	if sequential.horizonCapBytes() <= initial.horizonCapBytes() {
		t.Fatalf("sequential ceiling %d does not exceed initial %d", sequential.horizonCapBytes(), initial.horizonCapBytes())
	}
	if initial.usesConsumptionCredit() != sequential.usesConsumptionCredit() {
		t.Fatal("the upgrade changed the prefetch rule")
	}
}

// TestPlanWithoutModeKeepsLiteralNumbers pins the non-adaptive fallback: a plan
// built outside ResolveStage (bulk bursts, ReadStream, probe overrides) has no
// adaptive mode and executes its numbers literally.
func TestPlanWithoutModeKeepsLiteralNumbers(t *testing.T) {
	plan := ReadPlan{Stage: StageSequential, PrefetchChunks: 2, PrefetchDepth: 3}
	if plan.usesConsumptionCredit() {
		t.Fatal("a plan without an adaptive mode must not consume credit")
	}
	if plan.windowCapBytes() != 2*ChunkSize || plan.horizonCapBytes() != 6*ChunkSize {
		t.Fatalf("literal caps changed: %d/%d", plan.windowCapBytes(), plan.horizonCapBytes())
	}
}
