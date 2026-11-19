package read

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
)

func TestReadRequestKeepsOneCopyPerContext(t *testing.T) {
	base := context.Background()
	plan := ReadPlan{Intent: ReadIntentInteractive, WindowChunks: 1, MaxStreams: 3}
	ctx := WithReadPlan(base, plan)
	ctx = WithGrantedStreams(ctx, 1)

	if got, ok := readPlanFromContext(ctx); !ok || got != plan {
		t.Fatalf("plan = %+v ok=%v, want %+v", got, ok, plan)
	}
	if got := grantedStreams(ctx, 9); got != 1 {
		t.Fatalf("grantedStreams = %d, want 1", got)
	}
	// Rewriting one field must not disturb the others.
	next := WithReadPlan(ctx, ReadPlan{Intent: ReadIntentInteractive, WindowChunks: 4, MaxStreams: 1})
	if got := grantedStreams(next, 9); got != 1 {
		t.Fatalf("grantedStreams after plan rewrite = %d, want 1", got)
	}
	if got, _ := readPlanFromContext(next); got.WindowChunks != 4 {
		t.Fatalf("rewritten plan = %+v, want 4 window chunks", got)
	}
	// The parent request stays untouched (copy-on-write).
	if got, _ := readPlanFromContext(ctx); got.WindowChunks != 1 {
		t.Fatalf("parent plan mutated: %+v", got)
	}
	if _, ok := readPlanFromContext(base); ok {
		t.Fatal("bare context reports a plan")
	}
}

func TestReadRequestCarriesIntentHintAndCacheAccess(t *testing.T) {
	access := readcache.Access{Run: 7}
	ctx := WithReadIntent(context.Background(), ReadIntentBulk)
	ctx = WithAccessHint(ctx, AccessHint{SessionID: 11, RequestID: 12})
	ctx = WithCacheAccess(ctx, access)

	if got := readIntentFromContext(ctx); got != ReadIntentBulk {
		t.Fatalf("intent = %v, want bulk", got)
	}
	hint, ok := AccessHintFromContext(ctx)
	if !ok || hint.SessionID != 11 || hint.RequestID != 12 {
		t.Fatalf("hint = %+v ok=%v", hint, ok)
	}
	if got := cacheAccess(ctx); got != access {
		t.Fatalf("cacheAccess = %+v, want %+v", got, access)
	}
	if _, ok := AccessHintFromContext(context.Background()); ok {
		t.Fatal("bare context reports an access hint")
	}
}

func TestReadRequestPrefetchMarkers(t *testing.T) {
	if !readPrefetchEnabled(context.Background()) {
		t.Fatal("prefetch disabled by default")
	}
	if readPrefetchEnabled(WithoutReadPrefetch(context.Background())) {
		t.Fatal("WithoutReadPrefetch did not disable prefetch")
	}
	prefetch := WithPrefetchRead(context.Background())
	if !isPrefetchRead(prefetch) {
		t.Fatal("WithPrefetchRead did not mark the read")
	}
	// The marker and the disable flag are independent fields of the request.
	both := WithoutReadPrefetch(prefetch)
	if !isPrefetchRead(both) || readPrefetchEnabled(both) {
		t.Fatalf("markers collided: prefetch=%v enabled=%v", isPrefetchRead(both), readPrefetchEnabled(both))
	}
}

func TestReadRequestTimingSurvivesRequestRewrites(t *testing.T) {
	ctx, timing := withFetchTiming(context.Background())
	if fetchTimingFromContext(ctx) != timing {
		t.Fatal("timing not attached")
	}
	rewritten := WithReadPlan(ctx, ReadPlan{WindowChunks: 2})
	if fetchTimingFromContext(rewritten) != timing {
		t.Fatal("plan rewrite dropped the timing accumulator")
	}
	// ensureFetchTiming re-attaches a caller-supplied timing to a bare context.
	recovered := ensureFetchTiming(context.Background(), timing)
	if fetchTimingFromContext(recovered) != timing {
		t.Fatal("ensureFetchTiming did not attach timing")
	}
	if fetchTimingFromContext(ensureFetchTiming(context.Background(), nil)) != nil {
		t.Fatal("nil timing was attached")
	}
}

func TestReadRequestPriorityDefaults(t *testing.T) {
	bulk := ReadPlan{Intent: ReadIntentBulk}
	ctx := applyIntentPriority(WithReadPlan(context.Background(), bulk), bulk)
	if p := priority(ctx); p != PriorityLow {
		t.Fatalf("priority = %v, want low", p)
	}
	explicit := WithPriority(ctx, PriorityHigh)
	if p := priority(explicit); p != PriorityHigh {
		t.Fatalf("explicit priority = %v, want high", p)
	}
	if !prioritySet(explicit) || prioritySet(context.Background()) {
		t.Fatal("prioritySet misreports")
	}
}
