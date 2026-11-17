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
// keeps W×D chunks of lookahead ahead of its cursor. PrefetchMode records which
// rule produced those numbers for diagnostics; there is no growth state left to
// infer from it. A disabled plan keeps W and D at zero and prefetches nothing.
type ReadPlan struct {
	Intent         ReadIntent
	Stage          ReadStage
	MaxStreams     int
	WindowChunks   int
	PrefetchChunks int
	PrefetchDepth  int
	PrefetchMode   drive.PrefetchMode
	Split          drive.SplitPolicy
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
	Profile            drive.ReadPolicy
	AdaptiveDepth      int
	ParallelSequential bool
}

func (r PlanResolver) ResolveStage(stage ReadStage, intent ReadIntent, callerChunks int) ReadPlan {
	p := r.Profile
	plan := ReadPlan{Intent: intent, Stage: stage, MaxStreams: 1, WindowChunks: max(1, callerChunks)}
	if intent == ReadIntentBulk {
		plan.WindowChunks = byteChunks(p.Bulk.WindowBytes)
		plan.MaxStreams = p.Bulk.Streams
		plan.Split = p.Bulk.Split
		plan.PrefetchChunks = plan.WindowChunks
		plan.PrefetchDepth = 1
		return plan
	}
	access := p.Interactive.Initial
	if stage == StageSeek {
		access = p.Interactive.Seek
	}
	if stage == StageSequential {
		access = p.Interactive.Sequential
	}
	if access.Window.Mode == drive.WindowFixed {
		plan.WindowChunks = byteChunks(access.Window.Bytes)
	}
	plan.PrefetchMode = access.Prefetch.Mode
	switch access.Prefetch.Mode {
	case drive.PrefetchFixed:
		plan.PrefetchChunks = byteChunks(access.Prefetch.WindowBytes)
		plan.PrefetchDepth = access.Prefetch.Depth
	case drive.PrefetchAdaptive:
		// The adaptive rule executes a fixed pipeline rather than growing a
		// budget: one foreground window per active window slot, W×D chunks of
		// lookahead in total. A FUSE foreground window is one chunk, so the
		// permit depth decides how many single-chunk lookahead windows stay
		// ahead of the cursor.
		plan.PrefetchChunks = plan.WindowChunks
		plan.PrefetchDepth = max(1, min(4, r.AdaptiveDepth))
	}
	if r.ParallelSequential && intent == ReadIntentUnspecified && stage == StageSequential {
		plan.MaxStreams = p.Bulk.Streams
		plan.Split = p.Bulk.Split
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
