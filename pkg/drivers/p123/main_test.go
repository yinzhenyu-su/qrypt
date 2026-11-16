package p123

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestMain collapses the presigned part-upload retry backoff for the whole
// package. Tests that simulate a transient provider failure on one part keep
// running through every retry attempt, but no longer sleep through the
// exponential backoff; setting the seam before m.Run keeps the parallel tests
// race-free.
func TestMain(m *testing.M) {
	partRetryWait = func(ctx context.Context, _ int, _ time.Duration) error { return ctx.Err() }
	os.Exit(m.Run())
}
