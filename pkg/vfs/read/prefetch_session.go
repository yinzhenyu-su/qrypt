package read

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

// A paused handle keeps at most its current lookahead. Retire its scheduling
// worker after inactivity; later consumption can create a fresh one.
const prefetchSessionIdle = 30 * time.Second

type sessionFetch struct {
	load     *windowLoad
	revision uint64
}

// deliveredRange is one successful caller delivery: size bytes starting at
// offset. It is the mount layer's actual return value, never a predicted
// request end, so background progress, failures, stale requests and pages the
// kernel reads twice cannot credit it.
type deliveredRange struct {
	offset int64
	size   int64
}

func (d deliveredRange) end() int64 {
	if d.size <= 0 {
		return d.offset
	}
	return d.offset + d.size
}

// Forward-budget change reasons. They are debug labels only: they explain a
// transition, they never drive one.
const (
	budgetSeed    = "seed"
	budgetGrow    = "grow"
	budgetCap     = "cap"
	budgetFixed   = "fixed"
	budgetDisable = "disabled"
)

// prefetchSession owns scheduling, not another data cache or request budget.
// completed remembers fills even if the shared hot cache evicts them, so a
// paused reader can never cause an autonomous download/eviction/refetch loop.
//
// horizon and credit are the consumption-credit controller: horizon is the
// pass's forward byte budget H, and credit is the unique delivered suffix not
// yet spent on growing it. Both are O(1). When the plan does not use
// consumption credit (fixed, disabled, bulk, ReadStream) horizon stays at the
// stage's whole allowance and credit stays zero, so those paths execute their
// numbers literally.
type prefetchSession struct {
	mu        sync.Mutex
	wake      chan struct{}
	ctx       context.Context
	entry     drive.Entry
	plan      ReadPlan
	cursor    int64
	revision  uint64
	updated   time.Time
	pending   []sessionFetch
	completed map[int64]bool
	failed    uint64

	horizon   int64
	credit    int64
	delivered int64  // exclusive end of this pass's successful delivery
	budget    string // last forward-budget change reason
}

// budgetSnapshot is the controller state recorded with a change event. It is
// taken under the session lock and handed to the caller, so recording never
// re-acquires a lock the worker may hold.
type budgetSnapshot struct {
	ctx       context.Context
	stage     ReadStage
	mode      drive.PrefetchMode
	horizon   int64
	credit    int64
	delivered int64
	windowCap int64
	depthCap  int
	cursor    int64
	budget    string
}

func (p *prefetchSession) snapshotLocked() budgetSnapshot {
	return budgetSnapshot{
		ctx: p.ctx, stage: p.plan.Stage, mode: p.plan.PrefetchMode,
		horizon: p.horizon, credit: p.credit, delivered: p.delivered,
		windowCap: p.plan.windowCapBytes(), depthCap: p.plan.depthCap(),
		cursor: p.cursor, budget: p.budget,
	}
}

// update applies one successful read to the controller and returns the
// forward-budget transitions it caused together with the resulting state. The
// caller records them; scheduling never reads the projection back.
func (p *prefetchSession) update(ctx context.Context, entry drive.Entry, cursor int64, plan ReadPlan, consumed deliveredRange) ([]string, budgetSnapshot) {
	p.mu.Lock()
	changed := p.cursor != cursor || p.plan != plan
	previous := p.plan
	p.ctx, p.entry, p.cursor, p.plan = WithReadPlan(context.WithoutCancel(ctx), plan), entry, cursor, plan
	p.updated = time.Now()
	if changed {
		p.revision++
	}
	reasons := p.retuneLocked(previous)
	if reason := p.consumeLocked(consumed); reason != "" {
		reasons = append(reasons, reason)
	}
	p.budget = strings.Join(reasons, "+")
	snapshot := p.snapshotLocked()
	p.mu.Unlock()
	// A budget transition needs the worker even when the cursor does not move:
	// growth admits new range, and a clamped ceiling invalidates pending fills
	// beyond the new bound. A pure repeat delivery changes neither.
	if changed || len(reasons) > 0 {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
	return reasons, snapshot
}

// retuneLocked applies a plan or stage change to the forward budget and
// reports why the budget changed, if it did. A raised stage ceiling never
// expands H on its own; a lowered one clamps H and drops the old credit so a
// later upgrade cannot replay it.
func (p *prefetchSession) retuneLocked(previous ReadPlan) []string {
	if !p.plan.usesConsumptionCredit() {
		reason := budgetDisable
		if p.plan.PrefetchMode == drive.PrefetchFixed {
			reason = budgetFixed
		}
		// A switch out of adaptive is handled before the "budget unchanged"
		// check: credit earned under the growth rule must not survive into a
		// fixed window just because the ceilings happen to be equal.
		modeChanged := previous.PrefetchMode != "" && previous.PrefetchMode != p.plan.PrefetchMode
		limit := p.plan.horizonCapBytes()
		if !modeChanged && p.credit == 0 && p.horizon == limit {
			return nil
		}
		p.horizon, p.credit = limit, 0
		return []string{reason}
	}
	fresh := previous.PrefetchMode == "" || p.horizon == 0
	modeChanged := previous.PrefetchMode != p.plan.PrefetchMode && previous.PrefetchMode != ""
	if fresh || modeChanged {
		// A new pass, or a switch into the adaptive rule, starts from the seed
		// with no inherited credit.
		p.horizon, p.credit, p.delivered = p.plan.seedBytes(), 0, 0
		return []string{budgetSeed}
	}
	limit := p.plan.horizonCapBytes()
	// A lowered ceiling invalidates credit even when the current budget still
	// fits under it: the old credit was earned against a wider allowance.
	if limit < p.horizon || limit < previous.horizonCapBytes() || limit == 0 {
		p.horizon, p.credit = min(p.horizon, limit), 0
		return []string{budgetCap}
	}
	return nil
}

// consumeLocked advances the pass's delivered end by the unique positive
// suffix of one successful delivery and spends accumulated credit on growth.
func (p *prefetchSession) consumeLocked(consumed deliveredRange) string {
	if consumed.size <= 0 {
		return ""
	}
	end := consumed.end()
	if consumed.offset > p.delivered {
		// A forward gap was never delivered here; skipped bytes are not
		// consumption and cannot become credit.
		p.delivered = consumed.offset
	}
	if end <= p.delivered {
		return "" // A repeated page or an already counted suffix.
	}
	gained := end - p.delivered
	p.delivered = end
	if !p.plan.usesConsumptionCredit() {
		return ""
	}
	p.credit += gained
	if p.growLocked() {
		return budgetGrow
	}
	return ""
}

// growLocked expands H by at most one doubling per full budget of new
// consumption. A single large delivery may cross several steps, but a budget
// already spent on growth is never reused.
func (p *prefetchSession) growLocked() bool {
	limit := p.plan.horizonCapBytes()
	grew := false
	for p.horizon < limit && p.credit >= p.horizon {
		p.credit -= p.horizon
		p.horizon = min(2*p.horizon, limit)
		grew = true
	}
	if p.horizon >= limit {
		// Never bank credit against a later, larger ceiling: a stage upgrade
		// must not turn saved credit into an instant burst.
		p.credit = 0
	}
	return grew
}

// horizonChunks is the current forward budget H in whole cache chunks. A short
// EOF tail inside the last chunk is still read normally.
func (p *prefetchSession) horizonChunks() int64 {
	return max(1, (p.horizon+ChunkSize-1)/ChunkSize)
}

// windowChunks is W = the stage's single-window ceiling, and depthCap is D =
// the stage's active-window ceiling. H decides how much of them is used.
func (p *prefetchSession) windowChunks() int { return max(1, p.plan.PrefetchChunks) }

func (p *prefetchSession) depthCap() int { return p.plan.depthCap() }

// depthChunks is D = min(Dcap, ceil(H/Wcap)): the number of active windows the
// current budget pays for. A budget below one full window keeps exactly one
// active window, so a short pass never fans out over a slow link just because
// the stage allows more depth.
func (p *prefetchSession) depthChunks() int {
	window := int64(p.windowChunks())
	need := (p.horizonChunks() + window - 1) / window
	return int(min(int64(p.depthCap()), max(int64(1), need)))
}

// updateSessionPrefetch ties the worker to the same scope used for seek,
// invalidation and handle release. Unidentified callers keep bounded bursts.
//
// consumed is the successful delivery of this read. It is applied here, after
// the session identity check above, so a stale request, a concurrent hint or a
// released session can never credit the controller.
func (r *Reader) updateSessionPrefetch(ctx context.Context, entry drive.Entry, cursor int64, plan ReadPlan, consumed deliveredRange, create bool) bool {
	if _, streaming := ctx.Value(streamScopeKey{}).(*ReadScope); streaming {
		return false
	}
	hint, _ := AccessHintFromContext(ctx)
	if hint.SessionID == 0 || hint.RequestID == 0 || entry.Size <= 0 {
		return false
	}
	cacheKey := r.host.ReadCacheKey(entry)
	if cacheKey == "" {
		return true
	}
	// Serialize creation with session retirement and with newer requests. A
	// late foreground completion must not recreate a released session's work.
	r.state.sequence.mu.Lock()
	defer r.state.sequence.mu.Unlock()
	current, ok := r.state.sequence.reads[sequentialKey{cacheKey: cacheKey, session: hint.SessionID}]
	if !ok || current.requestID != hint.RequestID || hint.Concurrent || ctx.Err() != nil {
		return true
	}
	scope, release := r.state.scopeFor(ctx, cacheKey)
	for {
		scope.mu.Lock()
		if scope.ctx.Err() != nil {
			scope.mu.Unlock()
			release()
			return true
		}
		p := scope.prefetch
		if p == nil && !create {
			// Delivery accounting alone must never start a controller. A probe
			// that has not admitted a prefetch has no session to credit, and
			// creating one here would turn a short read into extra traffic.
			scope.mu.Unlock()
			release()
			return true
		}
		created := p == nil
		if created {
			p = &prefetchSession{wake: make(chan struct{}, 1), completed: make(map[int64]bool)}
			scope.prefetch = p
		}
		scope.mu.Unlock()
		// Never hold scope.mu while waiting for the planner mutex: scope
		// cancellation holds the registry lock and must remain independent
		// of speculative range selection and its shared admission locks.
		reasons, snapshot := p.update(ctx, entry, cursor, plan, consumed)
		if len(reasons) > 0 {
			r.recordSessionBudget(entry, reasons, snapshot)
		}
		scope.mu.Lock()
		current := scope.prefetch == p
		scope.mu.Unlock()
		if !current {
			continue // Idle retirement raced the update; create a fresh worker.
		}
		if created {
			go r.runSessionPrefetch(scope, p, release)
		} else {
			release()
		}
		return true
	}
}

func (r *Reader) runSessionPrefetch(scope *ReadScope, p *prefetchSession, release func()) {
	defer release()
	defer func() {
		scope.mu.Lock()
		if scope.prefetch == p {
			scope.prefetch = nil
		}
		scope.mu.Unlock()
	}()
	timer := time.NewTimer(prefetchSessionIdle)
	defer timer.Stop()
	for {
		// Capture before filling: a release racing a failed TryFetch must
		// still wake this worker, including releases from direct readers.
		capacity := r.state.scheduler.CapacityChanged()
		p.mu.Lock()
		if scope.ctx.Err() != nil {
			p.mu.Unlock()
			return
		}
		if !r.fillSessionPrefetch(scope, p) {
			capacity = nil // A full/paused horizon needs only consumption.
		}
		remaining := time.Until(p.updated.Add(prefetchSessionIdle))
		p.mu.Unlock()
		timer.Reset(max(time.Nanosecond, remaining))
		select {
		case <-scope.ctx.Done():
			return
		case <-p.wake:
		case <-capacity:
		case <-timer.C:
			// Retire under both locks; updates recheck scope identity after
			// acquiring the planner mutex without holding the scope lock.
			p.mu.Lock()
			scope.mu.Lock()
			expired := time.Since(p.updated) >= prefetchSessionIdle
			if expired && scope.prefetch == p {
				scope.prefetch = nil
			}
			scope.mu.Unlock()
			p.mu.Unlock()
			if expired {
				return
			}
		}
	}
}

// fillSessionPrefetch runs with p.mu held. The horizon is anchored at consumed
// data, never at the end of the last completed download. Both ready bytes and
// in-flight ranges occupy it; holes nearest the cursor are admitted first.
func (r *Reader) fillSessionPrefetch(scope *ReadScope, p *prefetchSession) bool {
	fileEnd := (p.entry.Size-1)/ChunkSize + 1
	// H (horizon) bounds the forward bytes; W and D bound each request and the
	// number of active requests, so a small budget never turns into a wide
	// Range. The horizon is anchored at consumed data, never at the end of the
	// last completed download.
	horizon := p.horizonChunks()
	window := int64(p.windowChunks())
	depth := p.depthChunks()
	end := min(p.cursor+horizon, fileEnd)
	reason := "covered"
	if logging.L.Enabled(logging.LevelDebug) {
		defer func() { r.recordSessionPrefetch(p, end, reason) }()
	}
	for index := range p.completed {
		if index < p.cursor || index >= end {
			delete(p.completed, index)
		}
	}
	active := p.pending[:0]
	for _, pending := range p.pending {
		select {
		case <-pending.load.done:
			for index := max(p.cursor, pending.load.start); index <= pending.load.end && index < end; index++ {
				if pending.load.chunkAvailable(index) {
					p.completed[index] = true
				}
			}
			if pending.load.err != nil {
				p.failed = max(p.failed, pending.revision)
			}
		default:
			active = append(active, pending)
		}
	}
	clear(p.pending[len(active):]) // Do not retain completed range buffers.
	p.pending = active
	if p.failed == p.revision {
		reason = "failed"
		return len(p.pending) > 0 // Only new consumption can retry a failure.
	}
	cacheKey := r.host.ReadCacheKey(p.entry)
	jobCtx := context.WithValue(p.ctx, streamScopeKey{}, scope)
	hint, _ := AccessHintFromContext(p.ctx)
	r.state.windows.mu.Lock()
	owned := 0
	for _, load := range r.state.windows.loads {
		if load.prefetch && load.fid == cacheKey && load.sessionID == hint.SessionID {
			owned++
		}
	}
	r.state.windows.mu.Unlock()
	owned = max(owned, len(p.pending))
	index := p.cursor
	for index < end {
		if scope.ctx.Err() != nil {
			reason = "canceled"
			return false
		}
		if p.completed[index] {
			index++
			continue
		}
		if _, ready := r.state.hotChunk(cacheKey, index); ready {
			p.completed[index] = true
			index++
			continue
		}
		if r.readChunkAvailableFor(cacheKey, index, 1) {
			index++
			continue
		}
		if owned >= depth {
			reason = "depth"
			return true
		}
		count := int(min(window, end-index))
		// Batch distant tail fills instead of opening a tiny Range after
		// every consumed block. The threshold follows this pass's actual
		// horizon, not the stage's maximum: a small budget must not wait for a
		// batch it can never fill. Cursor holes and EOF never wait.
		minimum := min(int(window), max(1, (int(horizon)+1)/2))
		if index > p.cursor && count < minimum && end < fileEnd {
			reason = "batch"
			return owned > 0
		}
		load := r.startPrefetchWindow(jobCtx, p.entry, index, count, true)
		if load == nil {
			reason = "not_admitted"
			return true
		}
		p.pending = append(p.pending, sessionFetch{load: load, revision: p.revision})
		owned++
		index = load.end + 1
	}
	return owned > 0
}

// Snapshot only data already resident in memory. Diagnostics must not touch
// the hot LRU, read durable cache data, or mistake eviction markers for bytes.
// p.mu is held; individual chunk readiness can advance during this snapshot.
func (r *Reader) recordSessionPrefetch(p *prefetchSession, end int64, reason string) {
	key := r.host.ReadCacheKey(p.entry)
	ready := make(map[int64]bool)
	r.state.fastPath.hot.mu.Lock()
	for index := p.cursor; index < end; index++ {
		_, ready[index] = r.state.fastPath.hot.chunks[ChunkKey(key, index)]
	}
	r.state.fastPath.hot.mu.Unlock()
	r.state.windows.mu.Lock()
	for _, load := range r.state.windows.loads {
		if load.fid != key {
			continue
		}
		for index := max(p.cursor, load.start); index <= load.end && index < end; index++ {
			ready[index] = ready[index] || load.chunkAvailable(index)
		}
	}
	r.state.windows.mu.Unlock()
	contiguous := p.cursor
	for contiguous < end && ready[contiguous] {
		contiguous++
	}
	stats := r.state.scheduler.Stats()
	extra := map[string]any{
		"cursor_chunk": p.cursor, "horizon_end_chunk": end, "ready_end_chunk": contiguous,
		"pending_windows": len(p.pending), "fill_stop": reason,
		"source_id":       r.source.Policy().SourceID,
		"source_admitted": stats.NormalInUse + stats.HighInUse,
		"object_admitted": r.state.scheduler.PerFileInUse(p.entry.ID),
		"prefetch_in_use": stats.PrefetchInUse,
	}
	// The dynamic plan sits beside the stage ceilings it may use: H is what the
	// session allows now, Wcap/Dcap what the stage would allow.
	for key, value := range sessionBudgetExtra(p.snapshotLocked()) {
		extra[key] = value
	}
	RecordChunkDetail(r.observer, p.ctx, p.entry, "prefetch_plan", p.cursor, 0, (end-p.cursor)*ChunkSize, (contiguous-p.cursor)*ChunkSize, time.Now(), extra, nil)
}

// sessionBudgetExtra projects the session's forward budget for diagnostics. It
// is read-only: scheduling never consults these numbers.
func sessionBudgetExtra(s budgetSnapshot) map[string]any {
	return map[string]any{
		"prefetch_mode":       string(s.mode),
		"budget_bytes":        s.horizon,
		"credit_bytes":        s.credit,
		"delivered_end":       s.delivered,
		"window_cap_bytes":    s.windowCap,
		"depth_cap":           s.depthCap,
		"budget_change":       "",
		"budget_known":        s.horizon > 0,
		"stage_ceiling_bytes": s.windowCap * int64(max(1, s.depthCap)),
	}
}

// recordSessionBudget records one forward-budget transition: seed, growth,
// clamp, mode switch or the fixed/disabled literal. The reasons are constant
// labels so a trace can be reconciled without holding per-chunk history.
func (r *Reader) recordSessionBudget(entry drive.Entry, reasons []string, s budgetSnapshot) {
	extra := sessionBudgetExtra(s)
	extra["budget_change"] = strings.Join(reasons, "+")
	RecordChunkDetail(r.observer, s.ctx, entry, "prefetch_credit", s.cursor, 0, 0, 0, time.Now(), extra, nil)
}
