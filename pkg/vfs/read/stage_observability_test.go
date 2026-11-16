package read

// The read event must explain which stage and window policy produced the
// measurement, and it must never carry a signed download URL or credential.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// eventRecorder keeps the read events a reader reports. Prefetch events are
// recorded from background goroutines, so the sink is synchronized.
type eventRecorder struct {
	noopObserver
	mu    sync.Mutex
	reads []recordedEvent
}

func (o *eventRecorder) record(phase string, extra map[string]any) {
	o.mu.Lock()
	o.reads = append(o.reads, recordedEvent{Phase: phase, Extra: extra})
	o.mu.Unlock()
}

func (o *eventRecorder) snapshot() []recordedEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]recordedEvent(nil), o.reads...)
}

type recordedEvent struct {
	Phase string
	Extra map[string]any
}

func (o *eventRecorder) DebugNextOpID() string { return "op" }

func (o *eventRecorder) DebugRecordRead(_ string, _ string, _ string, _, _, _ int64, _ string, _, _, _ int64, _ time.Time, extra map[string]any, _ error) {
	o.record("read", extra)
}

func (o *eventRecorder) DebugRecordReadDetail(_ context.Context, _ string, _ string, phase string, _, _, _ int64, _ time.Time, extra map[string]any, _ error) {
	o.record(phase, extra)
}

func (o *eventRecorder) DebugCacheCounters() (int64, int64) { return 0, 0 }

func TestReadEventReportsStageAndWindowPolicy(t *testing.T) {
	recorder := &eventRecorder{}
	r := newStagedReaderWithProfile(t, 8<<20, stageProfile())
	r.reader.observer = recorder
	// The first read opens the stream; the second is a seek into fresh data.
	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	r.read(t, sessionContext(1, 2), 12*ChunkSize, ChunkSize)

	var sawSeek bool
	for _, event := range recorder.snapshot() {
		if event.Extra == nil {
			continue
		}
		if event.Extra["session_id"] != uint64(1) {
			t.Fatalf("event %q lost session identity: %+v", event.Phase, event.Extra)
		}
		if _, ok := event.Extra["read_stage"]; !ok {
			t.Fatalf("event %q has no read_stage: %+v", event.Phase, event.Extra)
		}
		if _, ok := event.Extra["prefetch_depth"]; !ok {
			t.Fatalf("event %q has no prefetch_depth: %+v", event.Phase, event.Extra)
		}
		if event.Extra["read_stage"] != StageSeek.String() {
			continue
		}
		sawSeek = true
		if event.Extra["window_chunks"] != 1 {
			t.Fatalf("seek window = %v, want the 1-chunk stage window", event.Extra["window_chunks"])
		}
		if event.Extra["prefetch_chunks"] != 2 {
			t.Fatalf("seek prefetch window = %v, want the declared 2 chunks", event.Extra["prefetch_chunks"])
		}
	}
	if !sawSeek {
		t.Fatalf("no seek stage recorded: %+v", recorder.snapshot())
	}
}

func TestPrefetchSnapshotDoesNotPromoteOrInventReadyData(t *testing.T) {
	r := newStagedReader(t, 8*ChunkSize)
	recorder := &eventRecorder{}
	r.reader.observer = recorder
	r.state.putHotChunk("cache", 2, []byte("second"))
	r.state.putHotChunk("cache", 1, []byte("first"))
	r.state.fastPath.hot.mu.Lock()
	before := append([]string(nil), r.state.fastPath.hot.lru...)
	r.state.fastPath.hot.mu.Unlock()
	ctx := drive.WithDebugOperation(sessionContext(7, 9), drive.DebugOperation{OpID: "op"})
	p := &prefetchSession{ctx: ctx, entry: r.host.entry, cursor: 1, completed: map[int64]bool{3: true}}
	r.reader.recordSessionPrefetch(p, 4, "batch")
	events := recorder.snapshot()
	if len(events) != 1 || events[0].Phase != "prefetch_plan" {
		t.Fatalf("snapshot events: %+v", events)
	}
	extra := events[0].Extra
	if extra["ready_end_chunk"] != int64(3) || extra["horizon_end_chunk"] != int64(4) || extra["session_id"] != uint64(7) || extra["request_id"] != uint64(9) {
		t.Fatalf("eviction marker counted as resident data or identity lost: %+v", extra)
	}
	r.state.fastPath.hot.mu.Lock()
	defer r.state.fastPath.hot.mu.Unlock()
	if fmt.Sprint(before) != fmt.Sprint(r.state.fastPath.hot.lru) {
		t.Fatal("diagnostic snapshot changed cache eviction order")
	}
}

func TestReadEventExtraCarriesNoDownloadURL(t *testing.T) {
	// Debug output is what users paste into issues. A signed download URL or a
	// cookie must never appear in it.
	recorder := &eventRecorder{}
	r := newStagedReader(t, 8<<20)
	r.reader.observer = recorder
	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	events := recorder.snapshot()
	if len(events) == 0 {
		t.Fatal("no read events recorded")
	}
	for _, event := range events {
		if event.Phase == "read" {
			if _, ok := event.Extra["deliver_us"].(int64); !ok {
				t.Fatalf("missing delivery timing: %+v", event.Extra)
			}
		}
		rendered := strings.ToLower(renderExtra(event.Extra))
		for _, forbidden := range []string{"http://", "https://", "auth_key", "cookie", "token="} {
			if strings.Contains(rendered, forbidden) {
				t.Fatalf("read event leaked %q: %s", forbidden, rendered)
			}
		}
	}
}

func renderExtra(extra map[string]any) string {
	var b strings.Builder
	for key, value := range extra {
		fmt.Fprintf(&b, "%s=%v ", key, value)
	}
	return b.String()
}

var _ ReadObserver = (*eventRecorder)(nil)
var _ = vfstypes.DebugActiveOp{}
