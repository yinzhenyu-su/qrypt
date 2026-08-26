package vfs

import "sort"

// Capability names one optional consumer surface a filesystem exposes
// through a focused interface. pkg/core and the CLI gate on these instead
// of discovering surfaces by scattered type assertions, so the available
// set is enumerable and documented in one place.
type Capability string

const (
	// CapabilityStreamRead gates the streaming read surface (StreamReader).
	CapabilityStreamRead Capability = "stream_read"
	// CapabilityReleaseReadSession gates open-file hint cleanup
	// (ReadSessionReleaser).
	CapabilityReleaseReadSession Capability = "release_read_session"
	// CapabilityListPage gates cursor-paged directory listing (ListPager).
	CapabilityListPage Capability = "list_page"
	// CapabilityRawRead gates backend byte reads (RawReader).
	CapabilityRawRead Capability = "raw_read"
	// CapabilityPathCapabilities gates per-path action capability queries
	// (CapabilityReporter).
	CapabilityPathCapabilities Capability = "path_capabilities"
	// CapabilityMountReport gates mount introspection (MountReporter).
	CapabilityMountReport Capability = "mount_report"
	// CapabilityClearReadCache gates read-cache clearing (ReadCacheCleaner).
	CapabilityClearReadCache Capability = "clear_read_cache"
	// CapabilityFlushReadCache gates read-cache flushing (ReadCacheFlusher).
	CapabilityFlushReadCache Capability = "flush_read_cache"
	// CapabilityCloseReadCache gates read-cache teardown (ReadCacheCloser).
	CapabilityCloseReadCache Capability = "close_read_cache"
	// CapabilityUploadInspection gates pending-upload introspection
	// (UploadInspector).
	CapabilityUploadInspection Capability = "upload_inspection"
	// CapabilitySpace gates aggregate space queries (SpaceProvider).
	CapabilitySpace Capability = "space"
	// CapabilityMountSpace gates per-mount space breakdown
	// (MountSpaceProvider).
	CapabilityMountSpace Capability = "mount_space"
)

// fsCapabilities is the optional consumer surface implemented by both a
// single VFS and a Namespace. It is the single source of truth for what the
// filesystem layer can do beyond the core FileSystem/Lifecycle contract.
var fsCapabilities = []Capability{
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

// Capabler declares the optional consumer surfaces a filesystem provides.
type Capabler interface {
	Capabilities() []Capability
}

// Capabilities returns the consumer surfaces declared by fs in a stable
// order (nil when fs does not implement Capabler).
func Capabilities(fs any) []Capability {
	c, ok := fs.(Capabler)
	if !ok {
		return nil
	}
	return normalizeCapabilities(c.Capabilities())
}

// HasCapability reports whether fs declares one optional consumer surface.
func HasCapability(fs any, capability Capability) bool {
	for _, existing := range Capabilities(fs) {
		if existing == capability {
			return true
		}
	}
	return false
}

func normalizeCapabilities(caps []Capability) []Capability {
	if len(caps) == 0 {
		return nil
	}
	normalized := append([]Capability(nil), caps...)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	out := normalized[:0]
	for _, capability := range normalized {
		if len(out) == 0 || out[len(out)-1] != capability {
			out = append(out, capability)
		}
	}
	return out
}

func (v *VFS) Capabilities() []Capability       { return fsCapabilities }
func (n *Namespace) Capabilities() []Capability { return fsCapabilities }
