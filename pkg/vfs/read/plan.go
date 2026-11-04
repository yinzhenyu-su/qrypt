package read

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ReadPlan is the resolved scheduling plan for one read. Every field is
// concrete: no zero value means "keep the previous policy".
type ReadPlan struct {
	Intent        ReadIntent
	MaxStreams    int
	WindowChunks  int
	PrefetchDepth int
	CacheWrites   bool
}

// PlanOverride temporarily replaces the resolved stream count and window size.
// It exists for measurement tools (probe test read) that need to A/B read
// configurations without rebuilding the binary. Production paths never set it.
type PlanOverride struct {
	MaxStreams   int
	WindowChunks int
}

// PlanResolver resolves a caller intent and a driver profile into a concrete
// ReadPlan.
type PlanResolver struct {
	Profile drive.ReadProfile
}

// Resolve returns a concrete plan. defaultWindowChunks is the caller's
// existing window policy (the FUSE seek heuristic or the stream default); an
// explicit intent uses its profile window, while an unspecified intent keeps
// the caller's window.
func (r PlanResolver) Resolve(intent ReadIntent, fileSize int64, defaultWindowChunks int) ReadPlan {
	profile := r.Profile
	if profile == (drive.ReadProfile{}) {
		profile = drive.DefaultReadProfile()
	}
	switch intent {
	case ReadIntentBulk:
		streams := profile.BulkStreams
		if fileSize > 0 && fileSize < profile.ParallelThreshold {
			streams = 1
		}
		if streams < 1 {
			streams = 1
		}
		return ReadPlan{
			Intent:        intent,
			MaxStreams:    streams,
			WindowChunks:  windowOrDefault(profile.BulkWindowChunks, defaultWindowChunks),
			PrefetchDepth: profile.PrefetchDepth,
			CacheWrites:   false,
		}
	case ReadIntentProbe:
		return ReadPlan{
			Intent:        intent,
			MaxStreams:    1,
			WindowChunks:  windowOrDefault(profile.ProbeWindowChunks, defaultWindowChunks),
			PrefetchDepth: 0,
			CacheWrites:   false,
		}
	case ReadIntentInteractive:
		return ReadPlan{
			Intent:        intent,
			MaxStreams:    1,
			WindowChunks:  windowOrDefault(profile.InteractiveWindowChunks, defaultWindowChunks),
			PrefetchDepth: profile.PrefetchDepth,
			CacheWrites:   true,
		}
	default:
		// Unspecified keeps the pre-intent behavior: one stream, the caller's
		// window, prefetch enabled, and durable cache writes.
		prefetchDepth := profile.PrefetchDepth
		if prefetchDepth <= 0 {
			prefetchDepth = drive.DefaultReadPrefetchDepth
		}
		return ReadPlan{
			Intent:        ReadIntentUnspecified,
			MaxStreams:    1,
			WindowChunks:  windowOrDefault(0, defaultWindowChunks),
			PrefetchDepth: prefetchDepth,
			CacheWrites:   true,
		}
	}
}

func windowOrDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	if fallback > 0 {
		return fallback
	}
	return 1
}

type readPlanContextKey struct{}

// WithReadPlan attaches a resolved read plan to ctx. Later read-domain stages
// consume it.
func WithReadPlan(ctx context.Context, plan ReadPlan) context.Context {
	return context.WithValue(ctx, readPlanContextKey{}, plan)
}

func readPlanFromContext(ctx context.Context) (ReadPlan, bool) {
	plan, ok := ctx.Value(readPlanContextKey{}).(ReadPlan)
	return plan, ok
}

type grantedStreamsContextKey struct{}

// WithGrantedStreams records the stream count the scheduler actually granted
// for this read. Diagnostics use it instead of the plan's MaxStreams hint.
func WithGrantedStreams(ctx context.Context, streams int) context.Context {
	return context.WithValue(ctx, grantedStreamsContextKey{}, streams)
}

func grantedStreams(ctx context.Context, fallback int) int {
	if streams, ok := ctx.Value(grantedStreamsContextKey{}).(int); ok && streams > 0 {
		return streams
	}
	return fallback
}

// applyIntentPriority supplies the intent's default priority only when the
// caller did not set one explicitly.
func applyIntentPriority(ctx context.Context, plan ReadPlan) context.Context {
	if prioritySet(ctx) {
		return ctx
	}
	switch plan.Intent {
	case ReadIntentBulk:
		return WithPriority(ctx, PriorityLow)
	case ReadIntentInteractive, ReadIntentProbe:
		return WithPriority(ctx, PriorityHigh)
	default:
		return ctx
	}
}
