package clock

import (
	"testing"
	"time"
)

func TestFakeFiresTimerWhenAdvanced(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clk := NewFake(start)
	fired := clk.After(30 * time.Second)
	select {
	case <-fired:
		t.Fatal("timer fired before the clock advanced")
	default:
	}
	if got := clk.Pending(); got != 1 {
		t.Fatalf("pending timers = %d, want 1", got)
	}
	clk.Advance(10 * time.Second)
	select {
	case <-fired:
		t.Fatal("timer fired early after advancing 10s of 30s")
	default:
	}
	clk.Advance(20 * time.Second)
	select {
	case got := <-fired:
		if !got.Equal(start.Add(30 * time.Second)) {
			t.Fatalf("timer fired at %v, want %v", got, start.Add(30*time.Second))
		}
	default:
		t.Fatal("timer did not fire after the clock reached its deadline")
	}
	if got := clk.Pending(); got != 0 {
		t.Fatalf("pending timers = %d, want 0", got)
	}
}

func TestFakeFiresDueTimersInDeadlineOrder(t *testing.T) {
	clk := NewFake(time.Unix(0, 0))
	late := clk.After(20 * time.Second)
	early := clk.After(5 * time.Second)
	clk.Advance(30 * time.Second)

	first, ok := <-early
	if !ok {
		t.Fatal("early timer did not fire")
	}
	if want := time.Unix(5, 0); !first.Equal(want) {
		t.Fatalf("early timer fired at %v, want %v", first, want)
	}
	second, ok := <-late
	if !ok {
		t.Fatal("late timer did not fire")
	}
	if want := time.Unix(20, 0); !second.Equal(want) {
		t.Fatalf("late timer fired at %v, want %v", second, want)
	}
}

func TestFakeRejectsOutOfOrderDelivery(t *testing.T) {
	clk := NewFake(time.Unix(0, 0))
	a := clk.After(10 * time.Second)
	b := clk.After(10 * time.Second)
	clk.Advance(10 * time.Second)
	if first := <-a; !first.Equal(time.Unix(10, 0)) {
		t.Fatalf("first timer fired at %v", first)
	}
	if second := <-b; !second.Equal(time.Unix(10, 0)) {
		t.Fatalf("second timer fired at %v", second)
	}
}

func TestFakeAfterZeroFiresImmediately(t *testing.T) {
	clk := NewFake(time.Unix(3, 0))
	select {
	case got := <-clk.After(0):
		if !got.Equal(time.Unix(3, 0)) {
			t.Fatalf("zero timer fired at %v", got)
		}
	default:
		t.Fatal("zero timer did not fire immediately")
	}
	if got := clk.Pending(); got != 0 {
		t.Fatalf("pending timers = %d, want 0", got)
	}
}

func TestRealClockMatchesTimePackage(t *testing.T) {
	before := time.Now()
	now := Real.Now()
	if now.Before(before.Add(-time.Second)) || now.After(time.Now().Add(time.Second)) {
		t.Fatalf("Real.Now returned %v, outside the call window", now)
	}
	start := time.Now()
	<-Real.After(5 * time.Millisecond)
	if elapsed := time.Since(start); elapsed < 5*time.Millisecond {
		t.Fatalf("Real.After returned after %v, want at least 5ms", elapsed)
	}
}
