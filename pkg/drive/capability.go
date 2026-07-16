package drive

import "sort"

// Capability names one optional behavior a Driver may expose through a
// focused interface. The base Driver contract is intentionally omitted because
// every value passed here is already a Driver.
type Capability string

const (
	CapabilityWriter             Capability = "writer"
	CapabilitySourceUploader     Capability = "source_uploader"
	CapabilityResumableUploader  Capability = "resumable_uploader"
	CapabilitySpace              Capability = "space"
	CapabilityPathResolver       Capability = "path_resolver"
	CapabilityRemoteNameResolver Capability = "remote_name_resolver"
	CapabilityForeignEntries     Capability = "foreign_entries"
)

// Capabilities returns the driver-declared optional behavior in a stable order.
func Capabilities(d Driver) []Capability {
	if d == nil {
		return nil
	}
	return normalizeCapabilities(d.Capabilities())
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
