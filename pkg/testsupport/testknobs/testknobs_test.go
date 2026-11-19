package testknobs

import (
	"testing"
	"time"
)

// TestKnobsAreIdentityWithoutFasttest pins the default build: knobs must not
// perturb anything unless the fasttest tag opts in.
func TestKnobsAreIdentityWithoutFasttest(t *testing.T) {
	if Fast() {
		if got := FixtureBytes(64 << 10); got >= 64<<10 {
			t.Fatalf("fasttest FixtureBytes did not shrink: %d", got)
		}
		if got := Pause(time.Second); got != 0 {
			t.Fatalf("fasttest Pause = %s, want 0", got)
		}
		return
	}
	if got := FixtureBytes(4096); got != 4096 {
		t.Fatalf("FixtureBytes(4096) = %d, want identity", got)
	}
	if got := FixtureBytes(0); got != 1 {
		t.Fatalf("FixtureBytes(0) = %d, want 1", got)
	}
	if got := Pause(250 * time.Millisecond); got != 250*time.Millisecond {
		t.Fatalf("Pause = %s, want identity", got)
	}
	if got := Pause(0); got != 0 {
		t.Fatalf("Pause(0) = %s, want 0", got)
	}
}
