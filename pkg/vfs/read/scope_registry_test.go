package read

import (
	"context"
	"testing"
	"time"
)

func TestScopeRegistryCancelEntryPoints(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		cancel func(*ScopeRegistry)
	}{
		{"file", "invalidate", func(r *ScopeRegistry) { r.CancelFile("cache", "invalidate") }},
		{"session", "session_release", func(r *ScopeRegistry) { r.CancelSession(7, "session_release") }},
		{"all", "close", func(r *ScopeRegistry) { r.CancelAll("close") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := newScopeRegistry()
			ctx := WithAccessHint(context.Background(), AccessHint{SessionID: 7, RequestID: 1})
			scope, release := registry.Acquire(ctx, "cache")
			defer release()

			tc.cancel(registry)
			select {
			case <-scope.ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("scope was not canceled")
			}
			if got := scope.cancelReason(); got != tc.reason {
				t.Fatalf("cancel reason = %q, want %q", got, tc.reason)
			}
		})
	}
}
