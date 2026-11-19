package read

import "testing"

func TestWindowShapeStatsCountsIntentWindows(t *testing.T) {
	state := &State{windows: newWindowState()}
	state.beginWindowLoad("a", &windowLoad{fid: "a", start: 0, end: 3, intent: ReadIntentInteractive, done: make(chan struct{})})
	state.beginWindowLoad("b", &windowLoad{fid: "b", start: 4, end: 5, intent: ReadIntentBulk, done: make(chan struct{})})
	state.beginWindowLoad("c", &windowLoad{fid: "c", start: 6, end: 6, intent: ReadIntentUnspecified, done: make(chan struct{})})

	interactiveLoads, interactiveChunks, bulkLoads, bulkChunks := state.WindowShapeStats()
	if interactiveLoads != 2 || interactiveChunks != 5 {
		t.Fatalf("interactive counters = %d loads / %d chunks, want 2/5", interactiveLoads, interactiveChunks)
	}
	if bulkLoads != 1 || bulkChunks != 2 {
		t.Fatalf("bulk counters = %d loads / %d chunks, want 1/2", bulkLoads, bulkChunks)
	}
}
