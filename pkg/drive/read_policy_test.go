package drive

import (
	"reflect"
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
	d.policy.Bulk.Split.PartBytes = 8 << 20
	zero := int64(0)
	want := 8
	p, err := ResolveReadPolicy(d, &ReadOverride{MaxInFlight: &want, PartBytes: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if p.Effective.MaxInFlight != 2 || p.Effective.Bulk.Streams != 2 || p.Effective.Bulk.Split.PartBytes != 0 {
		t.Fatalf("effective = %+v", p.Effective)
	}
	if p.Recommended.Bulk.Split.PartBytes != 8<<20 || p.Origins["max_in_flight"] != "driver_limit" || p.Origins["bulk.streams"] != "driver_limit" {
		t.Fatalf("lost origin: %+v", p)
	}
}
func TestCompletePolicyRejectsZeroAndMalformedModes(t *testing.T) {
	if err := ValidateReadPolicy(ReadPolicy{}); err == nil {
		t.Fatal("incomplete profile accepted")
	}
	p := DefaultReadPolicy()
	p.Interactive.Seek.Prefetch = PrefetchPolicy{Mode: PrefetchDisabled, Depth: 1}
	if err := ValidateReadPolicy(p); err == nil {
		t.Fatal("disabled prefetch with depth accepted")
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
		"window mode":        func(p *ReadPolicy) { p.Interactive.Seek.Window.Mode = "unknown" },
		"caller size":        func(p *ReadPolicy) { p.Interactive.Seek.Window.Bytes = 1 },
		"empty fixed window": func(p *ReadPolicy) { p.Interactive.Initial.Window.Mode = WindowFixed },
		"oversized fixed window": func(p *ReadPolicy) {
			p.Interactive.Initial.Window = WindowPolicy{Mode: WindowFixed, Bytes: MaxReadWindowBytes + 1}
		},
		"prefetch mode":  func(p *ReadPolicy) { p.Interactive.Seek.Prefetch.Mode = "unknown" },
		"adaptive depth": func(p *ReadPolicy) { p.Interactive.Seek.Prefetch.Depth = 1 },
		"prefetch depth": func(p *ReadPolicy) {
			p.Interactive.Sequential.Prefetch = PrefetchPolicy{Mode: PrefetchFixed, WindowBytes: 1, Depth: 5}
		},
		"prefetch size": func(p *ReadPolicy) {
			p.Interactive.Sequential.Prefetch = PrefetchPolicy{Mode: PrefetchFixed, WindowBytes: MaxReadWindowBytes + 1, Depth: 1}
		},
		"empty bulk window":   func(p *ReadPolicy) { p.Bulk.WindowBytes = 0 },
		"bulk streams":        func(p *ReadPolicy) { p.Bulk.Streams = 33 },
		"file budget":         func(p *ReadPolicy) { p.MaxInFlight = 0 },
		"negative part":       func(p *ReadPolicy) { p.Bulk.Split.PartBytes = -1 },
		"oversized threshold": func(p *ReadPolicy) { p.Bulk.Split.MinParallelBytes = MaxReadWindowBytes + 1 },
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
