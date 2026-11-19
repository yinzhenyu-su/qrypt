package read

import (
	"context"
	"testing"
)

// Closing the durable read cache is not VFS teardown: cache-miss reads must
// still start scopes and fetch from the provider. Only State.Close ends the
// read path.
func TestCloseReadCacheKeepsReadPathAlive(t *testing.T) {
	s := NewState(nil)
	if err := s.CloseReadCache(); err != nil {
		t.Fatalf("CloseReadCache: %v", err)
	}
	scope, release := s.scopeFor(context.Background(), "cache-key")
	select {
	case <-scope.ctx.Done():
		release()
		t.Fatal("read scope canceled after CloseReadCache: closing the cache must not end the read path")
	default:
	}
	release()

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ended, releaseEnded := s.scopeFor(context.Background(), "cache-key-2")
	defer releaseEnded()
	select {
	case <-ended.ctx.Done():
	default:
		t.Fatal("read scope still live after State.Close: teardown must refuse new scopes")
	}
}
