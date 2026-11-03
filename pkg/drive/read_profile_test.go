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
