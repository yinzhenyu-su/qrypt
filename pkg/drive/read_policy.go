package drive

import (
	"fmt"
)

// InteractivePolicy is the streaming shape. There are only two knobs: how
// large the confirmed sequential window is, and how far ahead of the cursor
// the lookahead runs. Initial and seek reads always keep the caller's bounded
// window: a larger window only pays off once forward progress is confirmed.
type InteractivePolicy struct {
	// WindowBytes is the sequential read-ahead window. Zero keeps the caller's
	// bounded window, which is one chunk for a FUSE read and the caller's
	// request for a streaming reader.
	WindowBytes int64 `json:"window_bytes,omitempty"`
	// PrefetchWindowBytes is the lookahead window W. Zero keeps it equal to the
	// read window, which is the common case; a driver only sets it when its
	// lookahead should differ from the window it reads with. PrefetchDepth is
	// the depth D: the horizon is W×D chunks, and zero disables lookahead.
	PrefetchWindowBytes int64 `json:"prefetch_window_bytes,omitempty"`
	PrefetchDepth       int   `json:"prefetch_depth,omitempty"`
}
type BulkPolicy struct {
	WindowBytes int64 `json:"window_bytes"`
	Streams     int   `json:"streams"`
	// PartBytes is a strict logical part ceiling. Zero means even division.
	PartBytes int64 `json:"part_bytes,omitempty"`
	// MinParallelBytes is the smallest window worth splitting. Zero always
	// splits when more than one worker is admitted.
	MinParallelBytes int64 `json:"min_parallel_bytes,omitempty"`
}

// ReadPolicy contains complete driver recommendations. Unlike ReadOverride,
// it has no inheritance semantics and is validated before a source is opened.
type ReadPolicy struct {
	Interactive InteractivePolicy `json:"interactive"`
	Bulk        BulkPolicy        `json:"bulk"`
	MaxInFlight int               `json:"max_in_flight"`
	Fetch       FetchTimeouts     `json:"fetch"`
}

// ReadOverride is the only sparse input to policy resolution. Nil inherits;
// an explicit zero part or split threshold is meaningful.
type ReadOverride struct {
	MaxInFlight      *int
	BulkStreams      *int
	BulkWindowBytes  *int64
	PartBytes        *int64
	MinParallelBytes *int64
}
type ReadLimits struct {
	MaxInFlight int  `json:"max_in_flight"`
	Parallel    bool `json:"parallel"`
}
type ReadLimitProvider interface{ ReadLimits() ReadLimits }
type ReadPolicyProvider interface{ ReadPolicy() ReadPolicy }

// ResolvedReadPolicy records provenance separately from execution values.
// It is copied by value at source boundaries; callers cannot mutate the source.
type ResolvedReadPolicy struct {
	SourceID    string            `json:"source_id,omitempty"`
	Recommended ReadPolicy        `json:"recommended"`
	Limits      ReadLimits        `json:"limits"`
	Effective   ReadPolicy        `json:"effective"`
	Origins     map[string]string `json:"origins"`
}

const MaxReadWindowBytes int64 = 32 << 20

func DefaultReadPolicy() ReadPolicy {
	return ReadPolicy{
		Interactive: InteractivePolicy{PrefetchDepth: 4},
		Bulk:        BulkPolicy{WindowBytes: 8 << 20, Streams: 4, MinParallelBytes: 8 << 20},
		MaxInFlight: 4, Fetch: FetchTimeouts{Idle: DefaultFetchIdleTimeout, Total: DefaultFetchTotalTimeout},
	}
}
func ValidateReadPolicy(p ReadPolicy) error {
	size := func(field string, n int64, zero bool) error {
		if n < 0 || (!zero && n == 0) || n > MaxReadWindowBytes {
			return fmt.Errorf("read.%s: invalid byte size %d (maximum %d)", field, n, MaxReadWindowBytes)
		}
		return nil
	}
	if err := size("interactive.window", p.Interactive.WindowBytes, true); err != nil {
		return err
	}
	if p.Interactive.PrefetchDepth < 0 || p.Interactive.PrefetchDepth > 4 {
		return fmt.Errorf("read.interactive.prefetch_depth: must be 0..4")
	}
	if err := size("interactive.prefetch_window", p.Interactive.PrefetchWindowBytes, true); err != nil {
		return err
	}
	if p.Bulk.Streams < 1 || p.Bulk.Streams > 32 {
		return fmt.Errorf("read.bulk.streams: must be 1..32")
	}
	if p.MaxInFlight < 1 || p.MaxInFlight > 32 {
		return fmt.Errorf("read.max_in_flight: must be 1..32")
	}
	if err := size("bulk.window", p.Bulk.WindowBytes, false); err != nil {
		return err
	}
	if err := size("bulk.part_size", p.Bulk.PartBytes, true); err != nil {
		return err
	}
	if err := size("bulk.min_parallel_size", p.Bulk.MinParallelBytes, true); err != nil {
		return err
	}
	if p.Fetch.Idle <= 0 || p.Fetch.Total <= 0 || p.Fetch.Idle > p.Fetch.Total {
		return fmt.Errorf("read.fetch: require 0 < idle <= total")
	}
	return nil
}
func ResolveReadPolicy(d Driver, o *ReadOverride) (ResolvedReadPolicy, error) {
	p := DefaultReadPolicy()
	origin := "default"
	if provider, ok := d.(ReadPolicyProvider); ok {
		p = provider.ReadPolicy()
		origin = "driver"
	}
	if err := ValidateReadPolicy(p); err != nil {
		return ResolvedReadPolicy{}, fmt.Errorf("%T: %w", d, err)
	}
	out := ResolvedReadPolicy{Recommended: p, Effective: p, Origins: map[string]string{}}
	for _, k := range []string{"interactive.window", "interactive.prefetch_window", "interactive.prefetch_depth", "bulk.streams", "bulk.window", "bulk.part_size", "bulk.min_parallel_size", "max_in_flight", "fetch"} {
		out.Origins[k] = origin
	}
	limits := ReadLimits{MaxInFlight: 32, Parallel: HasCapability(d, CapabilityParallelRange) && !HasCapability(d, CapabilityInternalParallelRead)}
	if provider, ok := d.(ReadLimitProvider); ok {
		declared := provider.ReadLimits()
		if declared.MaxInFlight < 1 || declared.MaxInFlight > 32 {
			return out, fmt.Errorf("%T: invalid read limit %d", d, declared.MaxInFlight)
		}
		limits.MaxInFlight = declared.MaxInFlight
		limits.Parallel = limits.Parallel && declared.Parallel
	}
	out.Limits = limits
	if o != nil {
		if o.BulkStreams != nil {
			p.Bulk.Streams = *o.BulkStreams
			out.Origins["bulk.streams"] = "mount"
			if *o.BulkStreams > 1 && !limits.Parallel {
				return out, fmt.Errorf("read.bulk.streams: driver does not support shared parallel ranges")
			}
		}
		if o.MaxInFlight != nil {
			p.MaxInFlight = *o.MaxInFlight
			out.Origins["max_in_flight"] = "mount"
		}
		if o.BulkWindowBytes != nil {
			p.Bulk.WindowBytes = *o.BulkWindowBytes
			out.Origins["bulk.window"] = "mount"
		}
		if o.PartBytes != nil {
			p.Bulk.PartBytes = *o.PartBytes
			out.Origins["bulk.part_size"] = "mount"
		}
		if o.MinParallelBytes != nil {
			p.Bulk.MinParallelBytes = *o.MinParallelBytes
			out.Origins["bulk.min_parallel_size"] = "mount"
		}
	}
	if err := ValidateReadPolicy(p); err != nil {
		return out, err
	}
	if !limits.Parallel {
		p.Bulk.Streams = 1
		out.Origins["bulk.streams"] = "capability"
	}
	if p.MaxInFlight > limits.MaxInFlight {
		p.MaxInFlight = limits.MaxInFlight
		out.Origins["max_in_flight"] = "driver_limit"
	}
	if p.Bulk.Streams > limits.MaxInFlight {
		p.Bulk.Streams = limits.MaxInFlight
		out.Origins["bulk.streams"] = "driver_limit"
	}
	if p.Bulk.Streams > p.MaxInFlight {
		p.Bulk.Streams = p.MaxInFlight
		out.Origins["bulk.streams"] = "budget"
	}
	out.Effective = p
	return out, nil
}

// ReadFailure identifies why a fetch was abandoned, independently of its
// transport error. Only provider failures are grounds for edge penalties.
type ReadFailure string

const (
	ReadCallerCanceled ReadFailure = "caller_canceled"
	ReadSuperseded     ReadFailure = "superseded"
	ReadIdleStall      ReadFailure = "idle_stall"
	ReadTotalDeadline  ReadFailure = "total_deadline"
	ReadTruncated      ReadFailure = "truncated"
	ReadNetworkFailure ReadFailure = "network_failure"
)

func (f ReadFailure) Error() string { return "read: " + string(f) }
