package drive

// ReadProfile is a driver's recommended read scheduling defaults. The read
// domain consumes it to pick stream counts, window sizes and prefetch depth.
// Drivers that do not implement ReadProfiler get DefaultReadProfile, gated by
// the parallel-range capabilities below. A mount may optionally override the
// bulk stream count and window in its config; unset fields still inherit this
// driver profile.
type ReadProfile struct {
	// BulkStreams is the number of concurrent sub-ranges for a bulk read.
	BulkStreams int
	// BulkWindowChunks, InteractiveWindowChunks and ProbeWindowChunks are the
	// window sizes in 512 KiB chunks for each intent.
	BulkWindowChunks        int
	InteractiveWindowChunks int
	ProbeWindowChunks       int
	// PrefetchDepth is how many windows a sequential stream may read ahead.
	PrefetchDepth int
	// ParallelThreshold is the minimum file size that justifies splitting a
	// window into parallel sub-ranges.
	ParallelThreshold int64
}

const (
	DefaultBulkStreams             = 4
	DefaultBulkWindowChunks        = 8
	DefaultInteractiveWindowChunks = 2
	DefaultProbeWindowChunks       = 1
	DefaultReadPrefetchDepth       = 1
	// DefaultParallelThreshold is 8 MiB: below it the request overhead and
	// assembly cost outweigh the extra connections.
	DefaultParallelThreshold = int64(8 << 20)
)

// DefaultReadProfile returns the conservative defaults used when a driver does
// not provide its own profile.
func DefaultReadProfile() ReadProfile {
	return ReadProfile{
		BulkStreams:             DefaultBulkStreams,
		BulkWindowChunks:        DefaultBulkWindowChunks,
		InteractiveWindowChunks: DefaultInteractiveWindowChunks,
		ProbeWindowChunks:       DefaultProbeWindowChunks,
		PrefetchDepth:           DefaultReadPrefetchDepth,
		ParallelThreshold:       DefaultParallelThreshold,
	}
}

// ReadProfiler is an optional driver interface for overriding the read
// scheduling defaults. Zero fields keep the corresponding default.
type ReadProfiler interface {
	ReadProfile() ReadProfile
}

// ParallelReadLimiter is an optional driver interface that caps how many
// concurrent sub-ranges a single file may use.
type ParallelReadLimiter interface {
	MaxParallelReads() int
}

// ReadProfileInfo is the effective profile plus where its bulk stream count
// came from. Source is one of ReadProfileSourceDriver,
// ReadProfileSourceMountOverride, or ReadProfileSourceDriverLimiter.
type ReadProfileInfo struct {
	Profile ReadProfile
	Source  string
	Clamped bool
}

const (
	ReadProfileSourceDriver        = "driver"
	ReadProfileSourceMountOverride = "mount_override"
	ReadProfileSourceDriverLimiter = "driver_limiter"
)

// ReadProfileOf resolves the effective read profile for d. A driver must
// declare CapabilityParallelRange to use more than one bulk stream;
// CapabilityInternalParallelRead keeps the VFS single-stream because the
// driver already parallelizes internally.
func ReadProfileOf(d Driver) ReadProfile {
	return ResolveReadProfile(d, nil).Profile
}

// ReadProfileOfWithOverride resolves a driver profile with an optional
// per-mount override. Nil or zero override fields keep the driver's value.
func ReadProfileOfWithOverride(d Driver, override *ReadProfile) ReadProfile {
	return ResolveReadProfile(d, override).Profile
}

// ResolveReadProfile resolves precedence, capability gates, and the optional
// driver limiter. Override fields win over the driver profile; zero override
// fields inherit. The source reports the origin of the bulk stream count.
func ResolveReadProfile(d Driver, override *ReadProfile) ReadProfileInfo {
	profile := DefaultReadProfile()
	source := ReadProfileSourceDriver
	hasOverride := override != nil && hasReadProfileFields(*override)
	if d == nil {
		profile.BulkStreams = 1
		return ReadProfileInfo{Profile: profile, Source: source}
	}
	if profiler, ok := d.(ReadProfiler); ok {
		profile = mergeReadProfile(profile, profiler.ReadProfile())
	}
	if hasOverride {
		profile = mergeReadProfile(profile, *override)
		source = ReadProfileSourceMountOverride
	}
	if !HasCapability(d, CapabilityParallelRange) || HasCapability(d, CapabilityInternalParallelRead) {
		profile.BulkStreams = 1
	}
	clamped := false
	if limiter, ok := d.(ParallelReadLimiter); ok {
		if limit := limiter.MaxParallelReads(); limit > 0 && profile.BulkStreams > limit {
			profile.BulkStreams = limit
			clamped = true
			source = ReadProfileSourceDriverLimiter
		}
	}
	return ReadProfileInfo{Profile: normalizeReadProfile(profile), Source: source, Clamped: clamped}
}

func hasReadProfileFields(profile ReadProfile) bool {
	return profile.BulkStreams > 0 ||
		profile.BulkWindowChunks > 0 ||
		profile.InteractiveWindowChunks > 0 ||
		profile.ProbeWindowChunks > 0 ||
		profile.PrefetchDepth > 0 ||
		profile.ParallelThreshold > 0
}

func mergeReadProfile(base, override ReadProfile) ReadProfile {
	if override.BulkStreams > 0 {
		base.BulkStreams = override.BulkStreams
	}
	if override.BulkWindowChunks > 0 {
		base.BulkWindowChunks = override.BulkWindowChunks
	}
	if override.InteractiveWindowChunks > 0 {
		base.InteractiveWindowChunks = override.InteractiveWindowChunks
	}
	if override.ProbeWindowChunks > 0 {
		base.ProbeWindowChunks = override.ProbeWindowChunks
	}
	if override.PrefetchDepth > 0 {
		base.PrefetchDepth = override.PrefetchDepth
	}
	if override.ParallelThreshold > 0 {
		base.ParallelThreshold = override.ParallelThreshold
	}
	return base
}

func normalizeReadProfile(profile ReadProfile) ReadProfile {
	if profile.BulkStreams <= 0 {
		profile.BulkStreams = DefaultBulkStreams
	}
	if profile.BulkWindowChunks <= 0 {
		profile.BulkWindowChunks = DefaultBulkWindowChunks
	}
	if profile.InteractiveWindowChunks <= 0 {
		profile.InteractiveWindowChunks = DefaultInteractiveWindowChunks
	}
	if profile.ProbeWindowChunks <= 0 {
		profile.ProbeWindowChunks = DefaultProbeWindowChunks
	}
	if profile.PrefetchDepth < 0 {
		profile.PrefetchDepth = DefaultReadPrefetchDepth
	}
	if profile.ParallelThreshold <= 0 {
		profile.ParallelThreshold = DefaultParallelThreshold
	}
	return profile
}
