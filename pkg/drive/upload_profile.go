package drive

import "context"

// UploadProfile overrides driver upload tuning for one measurement. Zero
// fields keep the driver default.
type UploadProfile struct {
	// ChunkSize is the multipart part size in bytes.
	ChunkSize int64
	// Concurrency is the number of parts uploaded at the same time.
	Concurrency int
}

type uploadProfileContextKey struct{}

// WithUploadProfile attaches a measurement-only upload profile to ctx. Only
// probe benchmarks set it; production paths never do.
func WithUploadProfile(ctx context.Context, profile UploadProfile) context.Context {
	return context.WithValue(ctx, uploadProfileContextKey{}, profile)
}

// UploadProfileFromContext returns the measurement override, if any.
func UploadProfileFromContext(ctx context.Context) (UploadProfile, bool) {
	profile, ok := ctx.Value(uploadProfileContextKey{}).(UploadProfile)
	return profile, ok
}

// WithDefaults fills zero fields from def, so a caller can override only the
// knobs it cares about.
func (p UploadProfile) WithDefaults(def UploadProfile) UploadProfile {
	if p.ChunkSize <= 0 {
		p.ChunkSize = def.ChunkSize
	}
	if p.Concurrency <= 0 {
		p.Concurrency = def.Concurrency
	}
	return p
}

// UploadProfiler reports the upload knobs a driver accepts. A zero field means
// the driver cannot change that knob, so measurement tools keep the default.
type UploadProfiler interface {
	UploadProfile() UploadProfile
}
