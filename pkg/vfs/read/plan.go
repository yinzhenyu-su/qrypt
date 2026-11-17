package read

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ReadStage is the access stage inside one read session. It is orthogonal to
// the caller's intent: the intent says why data is read, the stage says where
// in the stream this read sits.
type ReadStage int

const (
	// StageInitial is a first read or a read that cannot be placed yet. It
	// keeps the intent's existing policy.
	StageInitial ReadStage = iota
	// StageSeek follows a discontinuity: the caller jumped somewhere new.
	StageSeek
	// StageSequential continues a confirmed forward run.
	StageSequential
)

func (s ReadStage) String() string {
	switch s {
	case StageSeek:
		return "seek"
	case StageSequential:
		return "sequential"
	default:
		return "initial"
	}
}

// ReadPlan is concrete. Window sizes use cache chunks only inside VFS;
// the shared executor receives byte ranges.
//
// PrefetchChunks (W) and PrefetchDepth (D) are executed literally: a session
// keeps W×D chunks of lookahead ahead of its cursor. A plan with either at zero
// prefetches nothing. Bulk plans also carry the part layout: PartBytes is a
// strict part ceiling (zero means an even split) and MinParallelBytes is the
// smallest window worth splitting at all.
type ReadPlan struct {
	Intent           ReadIntent
	Stage            ReadStage
	MaxStreams       int
	WindowChunks     int
	PrefetchChunks   int
	PrefetchDepth    int
	PartBytes        int64
	MinParallelBytes int64
}

// cacheWrites reports whether this read may write durable cache chunks. Bulk
// reads skip the durable cache so a full-file transfer does not evict content
// the streaming path may reuse.
func (p ReadPlan) cacheWrites() bool { return p.Intent != ReadIntentBulk }

// streaming reports whether the plan follows the streaming shape. The resolver
// has two shapes only: streaming (interactive or unspecified) and bulk.
func (p ReadPlan) streaming() bool { return p.Intent != ReadIntentBulk }

// prefetchHorizonChunks is W×D: the fixed lookahead a session executes. It is
// zero when prefetching is disabled.
func (p ReadPlan) prefetchHorizonChunks() int64 {
	if p.PrefetchChunks <= 0 || p.PrefetchDepth <= 0 {
		return 0
	}
	return int64(p.PrefetchChunks) * int64(p.PrefetchDepth)
}

// PlanOverride is restricted to measurement tools.
type PlanOverride struct {
	MaxStreams        int
	WindowChunks      int
	FetchIdleTimeout  time.Duration
	FetchTotalTimeout time.Duration
}

type PlanResolver struct {
	Profile drive.ReadPolicy
	// AdaptiveDepth caps the lookahead depth by the shared prefetch capacity.
	AdaptiveDepth int
}

func (r PlanResolver) ResolveStage(stage ReadStage, intent ReadIntent, callerChunks int) ReadPlan {
	p := r.Profile
	plan := ReadPlan{Intent: intent, Stage: stage, MaxStreams: 1, WindowChunks: max(1, callerChunks)}
	if intent == ReadIntentBulk {
		plan.WindowChunks = byteChunks(p.Bulk.WindowBytes)
		plan.MaxStreams = p.Bulk.Streams
		plan.PartBytes = p.Bulk.PartBytes
		plan.MinParallelBytes = p.Bulk.MinParallelBytes
		plan.PrefetchChunks = plan.WindowChunks
		plan.PrefetchDepth = 1
		return plan
	}
	// Streaming: initial and seek keep the caller's bounded window; only a
	// confirmed forward run may widen it. Lookahead is one window per slot:
	// W is the window and D is the driver's recommended depth.
	if stage == StageSequential && p.Interactive.WindowBytes > 0 {
		plan.WindowChunks = byteChunks(p.Interactive.WindowBytes)
	}
	if depth := p.Interactive.PrefetchDepth; depth > 0 {
		w := plan.WindowChunks
		if p.Interactive.PrefetchWindowBytes > 0 {
			w = byteChunks(p.Interactive.PrefetchWindowBytes)
		}
		plan.PrefetchChunks = w
		plan.PrefetchDepth = max(1, min(depth, r.AdaptiveDepth))
	}
	plan.WindowChunks = min(plan.WindowChunks, int(drive.MaxReadWindowBytes/ChunkSize))
	return plan
}
func byteChunks(n int64) int { return max(1, int((n+ChunkSize-1)/ChunkSize)) }

// applyIntentPriority supplies the intent's default priority only when the
// caller did not set one explicitly.
func applyIntentPriority(ctx context.Context, plan ReadPlan) context.Context {
	if prioritySet(ctx) {
		return ctx
	}
	switch plan.Intent {
	case ReadIntentBulk:
		return WithPriority(ctx, PriorityLow)
	case ReadIntentInteractive:
		return WithPriority(ctx, PriorityHigh)
	default:
		return ctx
	}
}
