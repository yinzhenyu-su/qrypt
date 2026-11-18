package read

import "testing"

func TestLookaheadAccountingSeparatesDedupeFromOverlap(t *testing.T) {
	state := NewState(nil)
	first := &windowLoad{fid: "file", start: 4, end: 6, done: make(chan struct{})}
	state.beginWindowLoad("first", first)
	if deduped, overlapping := state.LookaheadStats(); deduped != 0 || overlapping != 0 {
		t.Fatalf("fresh state = (%d, %d), want zero", deduped, overlapping)
	}
	second := &windowLoad{fid: "file", start: 6, end: 8, done: make(chan struct{})}
	state.beginWindowLoad("second", second)
	if _, overlapping := state.LookaheadStats(); overlapping != 1 {
		t.Fatalf("overlapping = %d, want the single shared chunk", overlapping)
	}
	state.noteDedupedLookahead(3)
	if deduped, _ := state.LookaheadStats(); deduped != 3 {
		t.Fatalf("deduped = %d, want 3", deduped)
	}
}

func TestWindowTakeoverPreservesOtherSessionsAndDependents(t *testing.T) {
	state := NewState(nil)
	load := &windowLoad{fid: "file", start: 0, end: 1, prefetch: true, sessionID: 1, done: make(chan struct{})}
	state.beginWindowLoad("key", load)
	detach, ok := state.attachWindow(load)
	if !ok {
		t.Fatal("cannot attach")
	}
	defer detach()
	if state.abandonWindow(load, 2, false) {
		t.Fatal("another session canceled the owner")
	}
	other, ok := state.attachWindow(load)
	if !ok {
		t.Fatal("cannot attach dependent")
	}
	if state.abandonWindow(load, 1, false) {
		t.Fatal("owner canceled another foreground dependency")
	}
	if state.abandonWindow(load, 0, true) {
		t.Fatal("scope cleanup canceled useful foreground work")
	}
	other()
	if !state.abandonWindow(load, 1, false) {
		t.Fatal("unshared owner could not take over")
	}
	if detach, ok := state.attachWindow(load); ok {
		detach()
		t.Fatal("abandoned load was rejoined")
	}
	fresh := &windowLoad{fid: "file", start: 0, end: 1, done: make(chan struct{})}
	state.beginWindowLoad("key", fresh)
	state.endWindowLoad(load)
	if state.windows.loads["key"] != fresh {
		t.Fatal("late old cleanup deleted the replacement")
	}
}
