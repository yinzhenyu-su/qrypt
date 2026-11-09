package read

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestPerFileLimitStableAcrossWindows pins the budget across windows of the
// same file. A partial window asking for fewer streams must not replace the
// live per-file channel and let a fifth sub-range through.
func TestPerFileLimitStableAcrossWindows(t *testing.T) {
	state := NewState(nil)
	ctx := context.Background()

	var releases []func()
	for i := 0; i < 4; i++ {
		release, err := state.acquireParallel(ctx, "file", 4)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()

	blockedCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	release, err := state.acquireParallel(blockedCtx, "file", 2)
	if err == nil {
		release()
		t.Fatal("a second window with a different limit acquired past the per-file cap")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked acquire error = %v, want context.DeadlineExceeded", err)
	}
}
