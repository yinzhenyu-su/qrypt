package task

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain verifies no goroutine leaks after every test in this package
// (manager dispatch loops, per-task workers).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
