package read

import (
	"context"
	"testing"
	"time"
)

func TestScopeForReusesAndReleases(t *testing.T) {
	state := NewState(nil)
	ctx := context.Background()

	first, releaseFirst := state.scopeFor(ctx, "cache")
	second, releaseSecond := state.scopeFor(ctx, "cache")
	if first != second {
		t.Fatal("scopeFor did not reuse the same file/session scope")
	}
	releaseFirst()
	if first.ctx.Err() != nil {
		t.Fatal("scope canceled while a reference remains")
	}
	releaseSecond()
	if first.ctx.Err() == nil {
		t.Fatal("scope not canceled after the last reference was released")
	}
}

func TestInvalidateFileCancelsScope(t *testing.T) {
	state := NewState(nil)
	scope, release := state.scopeFor(context.Background(), "cache")
	defer release()

	state.InvalidateFile("cache")
	waitScopeCanceled(t, scope)
	if got := scope.cancelReason(); got != "invalidate" {
		t.Fatalf("cancel reason = %q, want invalidate", got)
	}
}

func TestReleaseReadSessionCancelsScope(t *testing.T) {
	state := NewState(nil)
	ctx := WithAccessHint(context.Background(), AccessHint{SessionID: 7, RequestID: 1})
	scope, release := state.scopeFor(ctx, "cache")
	defer release()

	state.ReleaseReadSession(7)
	waitScopeCanceled(t, scope)
	if got := scope.cancelReason(); got != "session_release" {
		t.Fatalf("cancel reason = %q, want session_release", got)
	}
}

func TestCancelReadScopeMatchesSession(t *testing.T) {
	state := NewState(nil)
	ctx := WithAccessHint(context.Background(), AccessHint{SessionID: 8, RequestID: 1})
	scope, release := state.scopeFor(ctx, "cache")
	defer release()

	state.cancelReadScope(ctx, "cache", "caller")
	waitScopeCanceled(t, scope)
	if got := scope.cancelReason(); got != "caller" {
		t.Fatalf("cancel reason = %q, want caller", got)
	}
}

func TestCloseCancelsAllScopes(t *testing.T) {
	state := NewState(nil)
	scope, release := state.scopeFor(context.Background(), "cache")
	defer release()

	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	waitScopeCanceled(t, scope)
	if got := scope.cancelReason(); got != "close" {
		t.Fatalf("cancel reason = %q, want close", got)
	}
}

func TestCancelPrefetchForFileCancelsScope(t *testing.T) {
	state := NewState(nil)
	scope, release := state.scopeFor(context.Background(), "cache")
	defer release()

	state.CancelPrefetchForFile("cache")
	waitScopeCanceled(t, scope)
	if got := scope.cancelReason(); got != "caller" {
		t.Fatalf("cancel reason = %q, want caller", got)
	}
}

func waitScopeCanceled(t *testing.T, scope *ReadScope) {
	t.Helper()
	select {
	case <-scope.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("scope was not canceled")
	}
}
