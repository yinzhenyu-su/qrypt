package drive

import "testing"

type profilingDriver struct {
	*FakeDriver
	profile ReadProfile
}

func (d profilingDriver) ReadProfile() ReadProfile { return d.profile }

type limitedDriver struct {
	*FakeDriver
	max int
}

func (d limitedDriver) MaxParallelReads() int { return d.max }

func TestDefaultReadProfileIsConservative(t *testing.T) {
	profile := DefaultReadProfile()
	if profile.BulkStreams != DefaultBulkStreams {
		t.Fatalf("BulkStreams = %d, want %d", profile.BulkStreams, DefaultBulkStreams)
	}
	if profile.BulkWindowChunks != DefaultBulkWindowChunks {
		t.Fatalf("BulkWindowChunks = %d, want %d", profile.BulkWindowChunks, DefaultBulkWindowChunks)
	}
	if profile.InteractiveWindowChunks != DefaultInteractiveWindowChunks {
		t.Fatalf("InteractiveWindowChunks = %d, want %d", profile.InteractiveWindowChunks, DefaultInteractiveWindowChunks)
	}
	if profile.ProbeWindowChunks != DefaultProbeWindowChunks {
		t.Fatalf("ProbeWindowChunks = %d, want %d", profile.ProbeWindowChunks, DefaultProbeWindowChunks)
	}
	if profile.PrefetchDepth != DefaultReadPrefetchDepth {
		t.Fatalf("PrefetchDepth = %d, want %d", profile.PrefetchDepth, DefaultReadPrefetchDepth)
	}
	if profile.ParallelThreshold != DefaultParallelThreshold {
		t.Fatalf("ParallelThreshold = %d, want %d", profile.ParallelThreshold, DefaultParallelThreshold)
	}
}

func TestReadProfileOfGatesBulkStreamsOnCapability(t *testing.T) {
	plain := NewFakeDriver()
	if got := ReadProfileOf(plain).BulkStreams; got != 1 {
		t.Fatalf("without capability BulkStreams = %d, want 1", got)
	}

	parallel := NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange))
	if got := ReadProfileOf(parallel).BulkStreams; got != DefaultBulkStreams {
		t.Fatalf("with parallel capability BulkStreams = %d, want %d", got, DefaultBulkStreams)
	}

	internal := NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange, CapabilityInternalParallelRead))
	if got := ReadProfileOf(internal).BulkStreams; got != 1 {
		t.Fatalf("with internal parallel BulkStreams = %d, want 1", got)
	}
}

func TestReadProfileOfHonorsDriverLimiter(t *testing.T) {
	driver := limitedDriver{
		FakeDriver: NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange)),
		max:        2,
	}
	if got := ReadProfileOf(driver).BulkStreams; got != 2 {
		t.Fatalf("BulkStreams = %d, want limiter value 2", got)
	}
}

func TestReadProfileOfMergesDriverOverride(t *testing.T) {
	driver := profilingDriver{
		FakeDriver: NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange)),
		profile: ReadProfile{
			BulkStreams:      6,
			BulkWindowChunks: 4,
		},
	}
	got := ReadProfileOf(driver)
	if got.BulkStreams != 6 {
		t.Fatalf("BulkStreams = %d, want 6", got.BulkStreams)
	}
	if got.BulkWindowChunks != 4 {
		t.Fatalf("BulkWindowChunks = %d, want 4", got.BulkWindowChunks)
	}
	if got.InteractiveWindowChunks != DefaultInteractiveWindowChunks {
		t.Fatalf("InteractiveWindowChunks = %d, want default %d", got.InteractiveWindowChunks, DefaultInteractiveWindowChunks)
	}
}

func TestReadProfileOfNilDriverIsSingleStream(t *testing.T) {
	if got := ReadProfileOf(nil).BulkStreams; got != 1 {
		t.Fatalf("nil driver BulkStreams = %d, want 1", got)
	}
}

func TestResolveReadProfileOverridePrecedence(t *testing.T) {
	driver := profilingDriver{
		FakeDriver: NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange)),
		profile:    ReadProfile{BulkStreams: 3, BulkWindowChunks: 4},
	}
	override := ReadProfile{BulkStreams: 6, BulkWindowChunks: 16}
	got := ResolveReadProfile(driver, &override)
	if got.Profile.BulkStreams != 6 || got.Profile.BulkWindowChunks != 16 {
		t.Fatalf("profile = %+v, want override 6/16", got.Profile)
	}
	if got.Source != ReadProfileSourceMountOverride {
		t.Fatalf("source = %q, want %q", got.Source, ReadProfileSourceMountOverride)
	}
	if got.Clamped {
		t.Fatal("unexpected clamp without a limiter")
	}
}

func TestResolveReadProfilePartialOverrideInheritsDriver(t *testing.T) {
	driver := profilingDriver{
		FakeDriver: NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange)),
		profile:    ReadProfile{BulkStreams: 3, BulkWindowChunks: 4, PrefetchDepth: 2},
	}
	override := ReadProfile{BulkWindowChunks: 16}
	got := ResolveReadProfile(driver, &override)
	if got.Profile.BulkStreams != 3 {
		t.Fatalf("BulkStreams = %d, want driver value 3", got.Profile.BulkStreams)
	}
	if got.Profile.BulkWindowChunks != 16 {
		t.Fatalf("BulkWindowChunks = %d, want override 16", got.Profile.BulkWindowChunks)
	}
	if got.Profile.PrefetchDepth != 2 {
		t.Fatalf("PrefetchDepth = %d, want driver value 2", got.Profile.PrefetchDepth)
	}
}

func TestResolveReadProfileZeroOverrideKeepsDriverSource(t *testing.T) {
	driver := profilingDriver{
		FakeDriver: NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange)),
		profile:    ReadProfile{BulkStreams: 3},
	}
	override := ReadProfile{}
	got := ResolveReadProfile(driver, &override)
	if got.Profile.BulkStreams != 3 {
		t.Fatalf("BulkStreams = %d, want driver value 3", got.Profile.BulkStreams)
	}
	if got.Source != ReadProfileSourceDriver {
		t.Fatalf("source = %q, want %q", got.Source, ReadProfileSourceDriver)
	}
}

func TestResolveReadProfileLimiterClampReportsSource(t *testing.T) {
	driver := limitedDriver{
		FakeDriver: NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange)),
		max:        4,
	}
	override := ReadProfile{BulkStreams: 8}
	got := ResolveReadProfile(driver, &override)
	if got.Profile.BulkStreams != 4 {
		t.Fatalf("BulkStreams = %d, want limiter 4", got.Profile.BulkStreams)
	}
	if got.Source != ReadProfileSourceDriverLimiter || !got.Clamped {
		t.Fatalf("source=%q clamped=%v, want %q/true", got.Source, got.Clamped, ReadProfileSourceDriverLimiter)
	}
}

func TestResolveReadProfileInternalParallelForcesSingleStream(t *testing.T) {
	driver := NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange, CapabilityInternalParallelRead))
	override := ReadProfile{BulkStreams: 6, BulkWindowChunks: 16}
	got := ResolveReadProfile(driver, &override)
	if got.Profile.BulkStreams != 1 {
		t.Fatalf("BulkStreams = %d, want 1 for internal parallel", got.Profile.BulkStreams)
	}
	if got.Profile.BulkWindowChunks != 16 {
		t.Fatalf("BulkWindowChunks = %d, want override 16", got.Profile.BulkWindowChunks)
	}
}
