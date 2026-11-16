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
// PrefetchMode records which prefetch rule the stage resolved to, so later
// stages never infer it from a number's provenance. For an adaptive plan,
// PrefetchChunks and PrefetchDepth are the stage's allowed ceilings (one
// window, and the number of active windows), not the range to issue: a mount
// session starts from an adaptive seed and grows toward those ceilings only as
// the caller consumes new bytes. A fixed plan executes the two numbers
// literally, and a disabled plan prefetches nothing.
type ReadPlan struct {
	Intent         ReadIntent
	Stage          ReadStage
	MaxStreams     int
	WindowChunks   int
	PrefetchChunks int
	PrefetchDepth  int
	PrefetchMode   drive.PrefetchMode
	Split          drive.SplitPolicy
	CacheWrites    bool
}

// adaptiveSeedCeilingBytes bounds the session's first forward budget. The real
// seed is the smallest of the stage ceilings and this value.
const adaptiveSeedCeilingBytes int64 = 1 << 20

// usesConsumptionCredit reports whether a mount session, rather than the fixed
// stage plan, decides this read's forward range from delivered bytes.
func (p ReadPlan) usesConsumptionCredit() bool {
	return p.PrefetchMode == drive.PrefetchAdaptive && p.PrefetchChunks > 0 && p.PrefetchDepth > 0
}

// windowCapBytes is Wcap: the stage's largest single prefetch window.
func (p ReadPlan) windowCapBytes() int64 {
	if p.PrefetchChunks <= 0 {
		return 0
	}
	return int64(p.PrefetchChunks) * ChunkSize
}

// depthCap is Dcap: the stage's largest number of active prefetch windows. It
// stays a ceiling even for a fixed plan, where it is also the plan itself.
func (p ReadPlan) depthCap() int { return max(1, p.PrefetchDepth) }

// horizonCapBytes is Hcap = Wcap x Dcap: the largest forward range the stage
// allows, excluding the foreground read's own range.
func (p ReadPlan) horizonCapBytes() int64 { return p.windowCapBytes() * int64(p.depthCap()) }

// seedBytes is Hseed = min(Hcap, Wcap, 1 MiB) in whole cache chunks. It is the
// forward budget a fresh adaptive read pass starts from.
func (p ReadPlan) seedBytes() int64 {
	seed := min(p.horizonCapBytes(), p.windowCapBytes(), adaptiveSeedCeilingBytes)
	return max(int64(ChunkSize), seed/ChunkSize*ChunkSize)
}

// initialHorizonBytes is the forward budget a fresh read pass starts from: the
// adaptive seed when consumption credit applies, otherwise the stage's whole
// allowance because a fixed plan executes its numbers literally.
func (p ReadPlan) initialHorizonBytes() int64 {
	if p.usesConsumptionCredit() {
		return p.seedBytes()
	}
	return p.horizonCapBytes()
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
	plan := ReadPlan{Intent: intent, Stage: stage, MaxStreams: 1, WindowChunks: max(1, callerChunks), CacheWrites: intent != ReadIntentProbe && intent != ReadIntentBulk}
	if intent == ReadIntentBulk {
		plan.WindowChunks = byteChunks(p.Bulk.WindowBytes)
		plan.MaxStreams = p.Bulk.Streams
		plan.Split = p.Bulk.Split
		plan.PrefetchChunks = plan.WindowChunks
		plan.PrefetchDepth = 1
		return plan
	}
	if intent == ReadIntentProbe {
		plan.WindowChunks = 1
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
		plan.PrefetchChunks = plan.WindowChunks
		plan.PrefetchDepth = 1
		if plan.WindowChunks == 1 {
			plan.PrefetchDepth = max(1, min(4, r.AdaptiveDepth))
			if stage == StageSequential {
				plan.PrefetchChunks = SequentialPrefetchChunks
			}
		}
	}
	if r.ParallelSequential && intent == ReadIntentUnspecified && stage == StageSequential {
		plan.MaxStreams = p.Bulk.Streams
		plan.Split = p.Bulk.Split
	}
	plan.WindowChunks = min(plan.WindowChunks, int(drive.MaxReadWindowBytes/ChunkSize))
	return plan
}
func byteChunks(n int64) int { return max(1, int((n+ChunkSize-1)/ChunkSize)) }

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
