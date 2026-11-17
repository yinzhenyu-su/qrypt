package read

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
)

// readRequestKey is the single context key the read package uses to carry
// per-call read state. Keeping the whole request under one key means the read
// plan, the granted stream count, the prefetch marker and the caller's intent
// always travel together: a stage that rewrites the plan (seek, stage upgrade)
// rewrites one copy of the request instead of stacking a second context value
// that the old value can shadow.
type readRequestKey struct{}

// readRequest is the per-call read state attached to ctx. Values are replaced
// copy-on-write by the With* helpers, so a derived context never mutates its
// parent's request.
type readRequest struct {
	plan        ReadPlan
	planSet     bool
	granted     int
	prefetch    bool
	prefetchOff bool
	hint        AccessHint
	intent      ReadIntent
	cacheAccess readcache.Access
	priority    priorityValue
	timing      *fetchTiming
	session     uint64
}

func readRequestFrom(ctx context.Context) readRequest {
	req, _ := ctx.Value(readRequestKey{}).(readRequest)
	return req
}

func withReadRequest(ctx context.Context, req readRequest) context.Context {
	return context.WithValue(ctx, readRequestKey{}, req)
}

// updateReadRequest rewrites a single field group of the request and returns a
// context holding the new copy.
func updateReadRequest(ctx context.Context, fn func(*readRequest)) context.Context {
	req := readRequestFrom(ctx)
	fn(&req)
	return withReadRequest(ctx, req)
}

// withReadSession records the open-file session this read belongs to. Window
// joins use it to avoid waiting behind another session's lookahead.
func withReadSession(ctx context.Context, session uint64) context.Context {
	return updateReadRequest(ctx, func(req *readRequest) { req.session = session })
}

func readSession(ctx context.Context) uint64 { return readRequestFrom(ctx).session }

// WithReadPlan attaches a resolved read plan to ctx. Later read-domain stages
// consume it.
func WithReadPlan(ctx context.Context, plan ReadPlan) context.Context {
	return updateReadRequest(ctx, func(req *readRequest) {
		req.plan = plan
		req.planSet = true
	})
}

func readPlanFromContext(ctx context.Context) (ReadPlan, bool) {
	req := readRequestFrom(ctx)
	return req.plan, req.planSet
}

// WithGrantedStreams records the stream count the scheduler actually granted
// for this read. Diagnostics use it instead of the plan's MaxStreams hint.
func WithGrantedStreams(ctx context.Context, streams int) context.Context {
	return updateReadRequest(ctx, func(req *readRequest) { req.granted = streams })
}

func grantedStreams(ctx context.Context, fallback int) int {
	if streams := readRequestFrom(ctx).granted; streams > 0 {
		return streams
	}
	return fallback
}

// WithPrefetchRead marks a background prefetch read so foreground-only fault
// rules skip it.
func WithPrefetchRead(ctx context.Context) context.Context {
	return updateReadRequest(ctx, func(req *readRequest) { req.prefetch = true })
}

func isPrefetchRead(ctx context.Context) bool {
	return readRequestFrom(ctx).prefetch
}

// WithoutReadPrefetch disables speculative reads for this context.
func WithoutReadPrefetch(ctx context.Context) context.Context {
	return updateReadRequest(ctx, func(req *readRequest) { req.prefetchOff = true })
}

func readPrefetchEnabled(ctx context.Context) bool {
	return !readRequestFrom(ctx).prefetchOff
}

// AccessHint identifies one open-file read stream. It is optional: callers
// without a stable handle keep the legacy per-file access tracking.
type AccessHint struct {
	SessionID  uint64
	RequestID  uint64
	Concurrent bool
}

// WithAccessHint attaches an open-file read hint to ctx.
func WithAccessHint(ctx context.Context, hint AccessHint) context.Context {
	return updateReadRequest(ctx, func(req *readRequest) { req.hint = hint })
}

func accessHintFromContext(ctx context.Context) (AccessHint, bool) {
	hint := readRequestFrom(ctx).hint
	return hint, hint.SessionID != 0 && hint.RequestID != 0
}

// AccessHintFromContext returns the open-file read hint attached to ctx.
func AccessHintFromContext(ctx context.Context) (AccessHint, bool) {
	return accessHintFromContext(ctx)
}

// ReadIntent classifies why a caller is reading. The zero value is
// unspecified: existing callers keep the pre-intent single-stream behavior.
type ReadIntent int

const (
	ReadIntentUnspecified ReadIntent = iota
	ReadIntentInteractive
	ReadIntentBulk
)

func (i ReadIntent) String() string {
	switch i {
	case ReadIntentInteractive:
		return "interactive"
	case ReadIntentBulk:
		return "bulk"
	default:
		return ""
	}
}

// WithReadIntent attaches a read intent to ctx. The read domain resolves it
// into a ReadPlan; the intent itself is not user configuration.
func WithReadIntent(ctx context.Context, intent ReadIntent) context.Context {
	return updateReadRequest(ctx, func(req *readRequest) { req.intent = intent })
}

func readIntentFromContext(ctx context.Context) ReadIntent {
	return readRequestFrom(ctx).intent
}

// ReadIntentFromContext returns the attached intent, or
// ReadIntentUnspecified when none was set.
func ReadIntentFromContext(ctx context.Context) ReadIntent {
	return readIntentFromContext(ctx)
}

// WithCacheAccess attaches the read run this context's reads belong to. The read
// cache uses it to tell a chunk consumed by the pass that admitted it apart from
// one that a later pass came back for, so a sequential stream reading its own
// read-ahead does not promote its chunks as if they were reused.
func WithCacheAccess(ctx context.Context, access readcache.Access) context.Context {
	return updateReadRequest(ctx, func(req *readRequest) { req.cacheAccess = access })
}

// cacheAccess returns the read run attached to ctx, or the zero Access when
// none was set. A zero Access is treated by the cache as an unknown run, which
// counts as reuse.
func cacheAccess(ctx context.Context) readcache.Access {
	return readRequestFrom(ctx).cacheAccess
}

type priorityValue struct {
	value Priority
	set   bool
}

func WithPriority(ctx context.Context, p Priority) context.Context {
	return updateReadRequest(ctx, func(req *readRequest) {
		req.priority = priorityValue{value: p, set: true}
	})
}

func priority(ctx context.Context) Priority {
	return readRequestFrom(ctx).priority.value
}

func prioritySet(ctx context.Context) bool {
	return readRequestFrom(ctx).priority.set
}

// withFetchTiming attaches a per-call timing accumulator to ctx.
func withFetchTiming(ctx context.Context) (context.Context, *fetchTiming) {
	timing := &fetchTiming{}
	return updateReadRequest(ctx, func(req *readRequest) { req.timing = timing }), timing
}

func fetchTimingFromContext(ctx context.Context) *fetchTiming {
	return readRequestFrom(ctx).timing
}

// ensureFetchTiming re-attaches timing to a context supplied by a caller that
// does not carry it (ReadContext during a stream refill).
func ensureFetchTiming(ctx context.Context, timing *fetchTiming) context.Context {
	if timing == nil || fetchTimingFromContext(ctx) != nil {
		return ctx
	}
	return updateReadRequest(ctx, func(req *readRequest) { req.timing = timing })
}
