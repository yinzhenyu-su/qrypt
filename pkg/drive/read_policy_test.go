package drive

import (
	"reflect"
	"strings"
	"testing"
)

type policyTestDriver struct {
	*FakeDriver
	policy ReadPolicy
	limits ReadLimits
}

func (d policyTestDriver) ReadPolicy() ReadPolicy { return d.policy }
func (d policyTestDriver) ReadLimits() ReadLimits { return d.limits }
func TestPolicyResolutionSeparatesOverrideFromLimits(t *testing.T) {
	d := policyTestDriver{NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange)), DefaultReadPolicy(), ReadLimits{MaxInFlight: 2, Parallel: true}}
	d.policy.Bulk.PartBytes = 8 << 20
	zero := int64(0)
	want := 8
	p, err := ResolveReadPolicy(d, &ReadOverride{MaxInFlight: &want, PartBytes: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if p.Effective.MaxInFlight != 2 || p.Effective.Bulk.Streams != 2 || p.Effective.Bulk.PartBytes != 0 {
		t.Fatalf("effective = %+v", p.Effective)
	}
	if p.Recommended.Bulk.PartBytes != 8<<20 || p.Origins["max_in_flight"] != "driver_limit" || p.Origins["bulk.streams"] != "driver_limit" {
		t.Fatalf("lost origin: %+v", p)
	}
}
func TestCompletePolicyRejectsZeroAndMalformedModes(t *testing.T) {
	if err := ValidateReadPolicy(ReadPolicy{}); err == nil {
		t.Fatal("incomplete profile accepted")
	}
	p := DefaultReadPolicy()
	p.Interactive.PrefetchDepth = 5
	if err := ValidateReadPolicy(p); err == nil {
		t.Fatal("prefetch depth above four accepted")
	}
	p = DefaultReadPolicy()
	zero := 0
	d := policyTestDriver{NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange)), p, ReadLimits{MaxInFlight: 4, Parallel: true}}
	if _, err := ResolveReadPolicy(d, &ReadOverride{BulkStreams: &zero}); err == nil {
		t.Fatal("explicit streams zero treated as inheritance")
	}
}
func TestSingleStreamRecommendationCanPipeline(t *testing.T) {
	d := policyTestDriver{NewFakeDriver(), DefaultReadPolicy(), ReadLimits{MaxInFlight: 4}}
	d.policy.Bulk.Streams = 1
	p, err := ResolveReadPolicy(d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Effective.MaxInFlight != 4 || p.Effective.Bulk.Streams != 1 {
		t.Fatalf("single stream destroyed file budget: %+v", p)
	}
}

func TestPolicyValidationBoundaries(t *testing.T) {
	for name, change := range map[string]func(*ReadPolicy){
		"negative window": func(p *ReadPolicy) { p.Interactive.WindowBytes = -1 },
		"oversized window": func(p *ReadPolicy) {
			p.Interactive.WindowBytes = MaxReadWindowBytes + 1
		},
		"prefetch depth": func(p *ReadPolicy) { p.Interactive.PrefetchDepth = 5 },
		"prefetch size": func(p *ReadPolicy) {
			p.Interactive.PrefetchWindowBytes = MaxReadWindowBytes + 1
			p.Interactive.PrefetchDepth = 1
		},
		"empty bulk window":   func(p *ReadPolicy) { p.Bulk.WindowBytes = 0 },
		"bulk streams":        func(p *ReadPolicy) { p.Bulk.Streams = 33 },
		"file budget":         func(p *ReadPolicy) { p.MaxInFlight = 0 },
		"negative part":       func(p *ReadPolicy) { p.Bulk.PartBytes = -1 },
		"oversized threshold": func(p *ReadPolicy) { p.Bulk.MinParallelBytes = MaxReadWindowBytes + 1 },
		"idle over total":     func(p *ReadPolicy) { p.Fetch.Idle = p.Fetch.Total + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			p := DefaultReadPolicy()
			change(&p)
			if err := ValidateReadPolicy(p); err == nil {
				t.Fatal("invalid complete policy accepted")
			}
		})
	}
}

func TestPolicyProvenanceDoesNotChangeExecution(t *testing.T) {
	d := policyTestDriver{NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange)), DefaultReadPolicy(), ReadLimits{MaxInFlight: 32, Parallel: true}}
	baseline, err := ResolveReadPolicy(d, nil)
	if err != nil {
		t.Fatal(err)
	}
	window := baseline.Effective.Bulk.WindowBytes
	overridden, err := ResolveReadPolicy(d, &ReadOverride{BulkWindowBytes: &window})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(baseline.Effective, overridden.Effective) || baseline.Origins["bulk.window"] != "driver" || overridden.Origins["bulk.window"] != "mount" {
		t.Fatalf("origin changed execution: %+v / %+v", baseline, overridden)
	}
	d.limits.MaxInFlight = 0
	if _, err := ResolveReadPolicy(d, nil); err == nil {
		t.Fatal("invalid hard limit accepted")
	}
}

func TestInteractiveOverridesApplyWithOrigin(t *testing.T) {
	d := policyTestDriver{NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange)), DefaultReadPolicy(), ReadLimits{MaxInFlight: 32, Parallel: true}}
	window, prefetchWindow, depth := int64(4<<20), int64(8<<20), 2
	out, err := ResolveReadPolicy(d, &ReadOverride{
		InteractiveWindowBytes:         &window,
		InteractivePrefetchWindowBytes: &prefetchWindow,
		InteractivePrefetchDepth:       &depth,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := InteractivePolicy{WindowBytes: window, PrefetchWindowBytes: prefetchWindow, PrefetchDepth: depth}
	if out.Effective.Interactive != want {
		t.Fatalf("interactive = %+v, want %+v", out.Effective.Interactive, want)
	}
	for _, key := range []string{"interactive.window", "interactive.prefetch_window", "interactive.prefetch_depth"} {
		if out.Origins[key] != "mount" {
			t.Fatalf("origin[%s] = %q, want mount", key, out.Origins[key])
		}
	}
}

func TestResolveReadPolicyWarnsAboutUnreachableShapes(t *testing.T) {
	p := DefaultReadPolicy()
	p.MaxInFlight = 2
	p.Bulk.Streams = 4
	p.Interactive.PrefetchDepth = 3
	warnings := ReadPolicyWarnings(p)
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want two (bulk streams and prefetch depth)", warnings)
	}
	if !strings.Contains(warnings[0], "read.bulk.streams (4) exceeds max_in_flight (2)") {
		t.Fatalf("first warning = %q", warnings[0])
	}
	if !strings.Contains(warnings[1], "read.interactive.prefetch_depth (3)") {
		t.Fatalf("second warning = %q", warnings[1])
	}

	requested := DefaultReadPolicy()
	requested.Bulk.MinParallelBytes = requested.Bulk.WindowBytes + 1
	if warnings := ReadPolicyWarnings(requested); len(warnings) != 1 || !strings.Contains(warnings[0], "run single-stream") {
		t.Fatalf("min parallel warning = %v", warnings)
	}

	d := policyTestDriver{NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange)), DefaultReadPolicy(), ReadLimits{MaxInFlight: 32}}
	resolved, err := ResolveReadPolicy(d, nil)
	if err != nil {
		t.Fatalf("resolve default policy: %v", err)
	}
	if len(resolved.Warnings) != 0 {
		t.Fatalf("shipped default produced warnings: %v", resolved.Warnings)
	}
}
