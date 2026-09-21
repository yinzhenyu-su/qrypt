package vfs

import "testing"

func TestVFSAndNamespaceDeclareConsumerSurfaces(t *testing.T) {
	t.Parallel()
	all := []Capability{
		CapabilityStreamRead,
		CapabilityReleaseReadSession,
		CapabilityListPage,
		CapabilityRawRead,
		CapabilityPathCapabilities,
		CapabilityMountReport,
		CapabilityClearReadCache,
		CapabilityFlushReadCache,
		CapabilityCloseReadCache,
		CapabilityUploadInspection,
		CapabilitySpace,
		CapabilityMountSpace,
	}
	for _, fs := range []any{(*VFS)(nil), (*Namespace)(nil)} {
		for _, capability := range all {
			if !HasCapability(fs, capability) {
				t.Fatalf("%T missing consumer surface %q", fs, capability)
			}
		}
	}
}

func TestCapabilitiesRejectsNonCapabler(t *testing.T) {
	t.Parallel()
	var fs struct{}
	if got := Capabilities(fs); got != nil {
		t.Fatalf("capabilities of non-capabler = %v, want nil", got)
	}
	if HasCapability(fs, CapabilityStreamRead) {
		t.Fatal("non-capabler reported a capability")
	}
	if HasCapability(nil, CapabilityStreamRead) {
		t.Fatal("nil reported a capability")
	}
	if HasCapability((*VFS)(nil), Capability("bogus")) {
		t.Fatal("unknown capability reported")
	}
}

func TestCapabilitiesStableSortedOrder(t *testing.T) {
	t.Parallel()
	caps := Capabilities((*VFS)(nil))
	if len(caps) != len(fsCapabilities) {
		t.Fatalf("capabilities = %d, want %d", len(caps), len(fsCapabilities))
	}
	for i := 1; i < len(caps); i++ {
		if caps[i-1] >= caps[i] {
			t.Fatalf("capabilities not sorted at %d: %v", i, caps)
		}
	}
}
