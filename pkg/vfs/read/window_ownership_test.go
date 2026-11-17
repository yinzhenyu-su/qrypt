package read

import "testing"

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
