package drive

import "sort"

// Capability names one optional behavior a Driver may expose through a
// focused interface. The base Driver contract is intentionally omitted because
// every value passed here is already a Driver.
type Capability string

const (
	CapabilityWriter             Capability = "writer"
	CapabilityUploader           Capability = "uploader"
	CapabilityFileUploader       Capability = "file_uploader"
	CapabilitySpace              Capability = "space"
	CapabilityPathResolver       Capability = "path_resolver"
	CapabilityDebugger           Capability = "debugger"
	CapabilityHealth             Capability = "health"
	CapabilityRemoteNameResolver Capability = "remote_name_resolver"
	CapabilityForeignEntries     Capability = "foreign_entries"
)

// CapabilityReporter lets wrappers report the capabilities they intentionally
// expose even when their concrete method set contains fallback methods.
type CapabilityReporter interface {
	Capabilities() []Capability
}

// Capabilities returns the optional drive interfaces implemented by d in a
// stable order. Use this when reporting or testing driver extension behavior
// instead of scattering ad hoc type assertions across packages.
func Capabilities(d Driver) []Capability {
	if d == nil {
		return nil
	}
	if reporter, ok := d.(CapabilityReporter); ok {
		return normalizeCapabilities(reporter.Capabilities())
	}
	caps := make([]Capability, 0, 9)
	if _, ok := d.(Writer); ok {
		caps = append(caps, CapabilityWriter)
	}
	if _, ok := d.(Uploader); ok {
		caps = append(caps, CapabilityUploader)
	}
	if _, ok := d.(FileUploader); ok {
		caps = append(caps, CapabilityFileUploader)
	}
	if _, ok := d.(SpaceQuerier); ok {
		caps = append(caps, CapabilitySpace)
	}
	if _, ok := d.(PathResolver); ok {
		caps = append(caps, CapabilityPathResolver)
	}
	if _, ok := d.(Debugger); ok {
		caps = append(caps, CapabilityDebugger)
	}
	if _, ok := d.(HealthChecker); ok {
		caps = append(caps, CapabilityHealth)
	}
	if _, ok := d.(RemoteNameResolver); ok {
		caps = append(caps, CapabilityRemoteNameResolver)
	}
	if _, ok := d.(ForeignEntryLister); ok {
		caps = append(caps, CapabilityForeignEntries)
	}
	return normalizeCapabilities(caps)
}

// HasCapability reports whether d implements one optional drive interface.
func HasCapability(d Driver, capability Capability) bool {
	for _, existing := range Capabilities(d) {
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
