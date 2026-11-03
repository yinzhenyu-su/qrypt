package read

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

func TestReadIntentRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := ReadIntentFromContext(ctx); got != ReadIntentUnspecified {
		t.Fatalf("missing intent = %v, want unspecified", got)
	}
	for _, intent := range []ReadIntent{ReadIntentInteractive, ReadIntentBulk, ReadIntentProbe} {
		if got := ReadIntentFromContext(WithReadIntent(ctx, intent)); got != intent {
			t.Fatalf("intent round trip = %v, want %v", got, intent)
		}
	}
	if ReadIntentBulk.String() != "bulk" || ReadIntentProbe.String() != "probe" || ReadIntentInteractive.String() != "interactive" {
		t.Fatalf("unexpected intent strings: %q %q %q", ReadIntentBulk, ReadIntentProbe, ReadIntentInteractive)
	}
}

func TestPlanForUnspecifiedKeepsExistingBehavior(t *testing.T) {
	plan := planFor(ReadIntentUnspecified, drive.DefaultReadProfile(), 64<<20)
	if plan.streams != 1 || plan.windowChunks != 0 || plan.prefetchDepth != -1 || !plan.cacheWrites {
		t.Fatalf("unspecified plan = %+v, want existing-behavior sentinels", plan)
	}
}

func TestPlanForBulkUsesProfileAndThreshold(t *testing.T) {
	profile := drive.DefaultReadProfile()
	profile.BulkStreams = 4
	profile.ParallelThreshold = 8 << 20

	large := planFor(ReadIntentBulk, profile, 16<<20)
	if large.streams != 4 || large.windowChunks != profile.BulkWindowChunks || large.prefetchDepth != profile.PrefetchDepth || large.cacheWrites {
		t.Fatalf("large bulk plan = %+v", large)
	}

	small := planFor(ReadIntentBulk, profile, 1<<20)
	if small.streams != 1 {
		t.Fatalf("small bulk streams = %d, want 1", small.streams)
	}
}

func TestPlanForInteractiveAndProbe(t *testing.T) {
	profile := drive.DefaultReadProfile()

	interactive := planFor(ReadIntentInteractive, profile, 16<<20)
	if interactive.streams != 1 || interactive.windowChunks != profile.InteractiveWindowChunks || !interactive.cacheWrites {
		t.Fatalf("interactive plan = %+v", interactive)
	}

	probe := planFor(ReadIntentProbe, profile, 16<<20)
	if probe.streams != 1 || probe.windowChunks != profile.ProbeWindowChunks || probe.prefetchDepth != 0 || probe.cacheWrites {
		t.Fatalf("probe plan = %+v", probe)
	}
}

type planCaptureHost struct {
	entry       drive.Entry
	data        []byte
	cacheKey    string
	plan        readPlan
	ok          bool
	reads       int
	priority    Priority
	prioritySet bool
	mu          sync.Mutex
}

func (h *planCaptureHost) Resolve(context.Context, string) (drive.Entry, error) {
	return h.entry, nil
}

func (*planCaptureHost) PendingUpload(string) (vfstypes.PendingUpload, bool, error) {
	return vfstypes.PendingUpload{}, false, nil
}

func (h *planCaptureHost) ReadCacheKey(drive.Entry) string { return h.cacheKey }

func (h *planCaptureHost) DriverRead(ctx context.Context, _ drive.Entry, offset, size int64) (io.ReadCloser, error) {
	h.mu.Lock()
	h.plan, h.ok = readPlanFromContext(ctx)
	h.priority, h.prioritySet = priority(ctx), prioritySet(ctx)
	h.reads++
	h.mu.Unlock()
	end := offset + size
	if end > int64(len(h.data)) {
		end = int64(len(h.data))
	}
	if offset > end {
		offset = end
	}
	return io.NopCloser(bytes.NewReader(h.data[offset:end])), nil
}

func (h *planCaptureHost) readCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reads
}

func TestReadCarriesReadPlan(t *testing.T) {
	host := &planCaptureHost{entry: drive.Entry{ID: "id", Size: 1, ModTime: time.Now()}, data: []byte("x")}
	profile := drive.DefaultReadProfile()
	profile.BulkStreams = 4
	profile.ParallelThreshold = 1
	reader := NewReader(ReaderDeps{Host: host, State: NewState(nil), Profile: profile})

	rc, err := reader.Read(WithReadIntent(context.Background(), ReadIntentBulk), "/file", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !host.ok {
		t.Fatal("read plan missing from driver read context")
	}
	if host.plan.intent != ReadIntentBulk || host.plan.streams != 4 || host.plan.cacheWrites {
		t.Fatalf("read plan = %+v, want bulk/4/no-cache", host.plan)
	}
	if !host.prioritySet || host.priority != PriorityLow {
		t.Fatalf("priority = %v (set=%v), want low", host.priority, host.prioritySet)
	}
}

func TestApplyIntentPriority(t *testing.T) {
	bulk := applyIntentPriority(context.Background(), readPlan{intent: ReadIntentBulk})
	if !prioritySet(bulk) || priority(bulk) != PriorityLow {
		t.Fatalf("bulk priority = %v, want low", priority(bulk))
	}
	interactive := applyIntentPriority(context.Background(), readPlan{intent: ReadIntentInteractive})
	if !prioritySet(interactive) || priority(interactive) != PriorityHigh {
		t.Fatalf("interactive priority = %v, want high", priority(interactive))
	}
	explicit := applyIntentPriority(WithPriority(context.Background(), PriorityNormal), readPlan{intent: ReadIntentBulk})
	if !prioritySet(explicit) || priority(explicit) != PriorityNormal {
		t.Fatalf("explicit priority = %v, want normal", priority(explicit))
	}
	unspecified := applyIntentPriority(context.Background(), readPlan{})
	if prioritySet(unspecified) {
		t.Fatal("unspecified intent should not set a priority")
	}
}

func TestReadBulkUsesParallelStreams(t *testing.T) {
	data := bytes.Repeat([]byte("a"), ChunkSize*4)
	host := &planCaptureHost{
		entry:    drive.Entry{ID: "id", Size: int64(len(data)), ModTime: time.Now()},
		data:     data,
		cacheKey: "cache",
	}
	profile := drive.DefaultReadProfile()
	profile.BulkStreams = 2
	profile.ParallelThreshold = 1
	reader := NewReader(ReaderDeps{Host: host, State: NewState(nil), Profile: profile})

	rc, err := reader.Read(WithReadIntent(context.Background(), ReadIntentBulk), "/file", 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if host.reads != 2 {
		t.Fatalf("driver reads = %d, want 2 parallel sub-ranges", host.reads)
	}
	if !host.ok || host.plan.streams != 2 {
		t.Fatalf("read plan = %+v, want 2 streams", host.plan)
	}
}

func TestReadStreamCarriesReadPlan(t *testing.T) {
	host := &planCaptureHost{entry: drive.Entry{ID: "id", Size: 1, ModTime: time.Now()}, data: []byte("x")}
	reader := NewReader(ReaderDeps{Host: host, State: NewState(nil), Profile: drive.DefaultReadProfile()})

	rc, err := reader.ReadStream(WithReadIntent(context.Background(), ReadIntentInteractive), "/file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !host.ok {
		t.Fatal("read plan missing from stream read context")
	}
	if host.plan.intent != ReadIntentInteractive || host.plan.streams != 1 || !host.plan.cacheWrites {
		t.Fatalf("stream plan = %+v, want interactive/single/cache", host.plan)
	}
}

func TestHintedSequentialLargeReadUpgradesToBulk(t *testing.T) {
	data := bytes.Repeat([]byte("a"), ChunkSize*16)
	host := &planCaptureHost{
		entry:    drive.Entry{ID: "id", Size: int64(len(data)), ModTime: time.Now()},
		data:     data,
		cacheKey: "cache",
	}
	profile := drive.DefaultReadProfile()
	profile.BulkStreams = 4
	profile.ParallelThreshold = 1
	reader := NewReader(ReaderDeps{Host: host, State: NewState(nil), Profile: profile})

	first := WithAccessHint(WithoutReadPrefetch(context.Background()), AccessHint{SessionID: 1, RequestID: 1})
	rc, err := reader.Read(first, "/file", 0, ChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	second := WithAccessHint(WithoutReadPrefetch(context.Background()), AccessHint{SessionID: 1, RequestID: 2})
	rc, err = reader.Read(second, "/file", ChunkSize, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	if !host.ok {
		t.Fatal("read plan missing from driver read context")
	}
	if host.plan.intent != ReadIntentBulk || host.plan.streams != 4 || !host.plan.cacheWrites {
		t.Fatalf("adaptive plan = %+v, want bulk/4/cache", host.plan)
	}
	if !host.prioritySet || host.priority != PriorityLow {
		t.Fatalf("adaptive priority = %v (set=%v), want low", host.priority, host.prioritySet)
	}
}

func TestReadStreamPrefetchesNextWindow(t *testing.T) {
	data := bytes.Repeat([]byte("a"), ChunkSize*8)
	host := &planCaptureHost{
		entry:    drive.Entry{ID: "id", Size: int64(len(data)), ModTime: time.Now()},
		data:     data,
		cacheKey: "cache",
	}
	profile := drive.DefaultReadProfile()
	profile.BulkStreams = 1
	profile.BulkWindowChunks = 1
	profile.ParallelThreshold = 1
	reader := NewReader(ReaderDeps{Host: host, State: NewState(nil), Profile: profile})

	rc, err := reader.ReadStream(WithReadIntent(context.Background(), ReadIntentBulk), "/file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Read(make([]byte, ChunkSize)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for host.readCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := host.readCount(); got < 2 {
		t.Fatalf("driver reads = %d, want the next-window prefetch", got)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProbeReadDoesNotPrefetch(t *testing.T) {
	data := bytes.Repeat([]byte("a"), ChunkSize*8)
	host := &planCaptureHost{
		entry:    drive.Entry{ID: "id", Size: int64(len(data)), ModTime: time.Now()},
		data:     data,
		cacheKey: "cache",
	}
	reader := NewReader(ReaderDeps{Host: host, State: NewState(nil), Profile: drive.DefaultReadProfile()})

	rc, err := reader.Read(WithReadIntent(context.Background(), ReadIntentProbe), "/file", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	time.Sleep(50 * time.Millisecond)
	if got := host.readCount(); got != 1 {
		t.Fatalf("driver reads = %d, want 1 (probe must not prefetch)", got)
	}
}

func TestReadStreamProbeDoesNotPrefetch(t *testing.T) {
	data := bytes.Repeat([]byte("a"), ChunkSize*8)
	host := &planCaptureHost{
		entry:    drive.Entry{ID: "id", Size: int64(len(data)), ModTime: time.Now()},
		data:     data,
		cacheKey: "cache",
	}
	reader := NewReader(ReaderDeps{Host: host, State: NewState(nil), Profile: drive.DefaultReadProfile()})

	rc, err := reader.ReadStream(WithReadIntent(context.Background(), ReadIntentProbe), "/file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Read(make([]byte, ChunkSize)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := host.readCount(); got != 1 {
		t.Fatalf("driver reads = %d, want 1 (probe stream must not prefetch)", got)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadStreamWithoutReadPrefetchDoesNotPrefetch(t *testing.T) {
	data := bytes.Repeat([]byte("a"), ChunkSize*8)
	host := &planCaptureHost{
		entry:    drive.Entry{ID: "id", Size: int64(len(data)), ModTime: time.Now()},
		data:     data,
		cacheKey: "cache",
	}
	reader := NewReader(ReaderDeps{Host: host, State: NewState(nil), Profile: drive.DefaultReadProfile()})

	ctx := WithoutReadPrefetch(WithReadIntent(context.Background(), ReadIntentBulk))
	rc, err := reader.ReadStream(ctx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Read(make([]byte, ChunkSize)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := host.readCount(); got != 1 {
		t.Fatalf("driver reads = %d, want 1 (prefetch disabled)", got)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
}
