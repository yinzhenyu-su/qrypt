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
	profile := drive.DefaultReadProfile()
	plan := PlanResolver{Profile: profile}.Resolve(ReadIntentUnspecified, 64<<20, 2)
	if plan.Streams != 1 || plan.WindowChunks != 2 || plan.PrefetchDepth != profile.PrefetchDepth || !plan.CacheWrites {
		t.Fatalf("unspecified plan = %+v, want one stream, caller window, prefetch, cache", plan)
	}
}

func TestPlanForBulkUsesProfileAndThreshold(t *testing.T) {
	profile := drive.DefaultReadProfile()
	profile.BulkStreams = 4
	profile.ParallelThreshold = 8 << 20

	large := PlanResolver{Profile: profile}.Resolve(ReadIntentBulk, 16<<20, 2)
	if large.Streams != 4 || large.WindowChunks != profile.BulkWindowChunks || large.PrefetchDepth != profile.PrefetchDepth || large.CacheWrites {
		t.Fatalf("large bulk plan = %+v", large)
	}

	small := PlanResolver{Profile: profile}.Resolve(ReadIntentBulk, 1<<20, 2)
	if small.Streams != 1 {
		t.Fatalf("small bulk streams = %d, want 1", small.Streams)
	}
}

func TestPlanForInteractiveAndProbe(t *testing.T) {
	profile := drive.DefaultReadProfile()

	interactive := PlanResolver{Profile: profile}.Resolve(ReadIntentInteractive, 16<<20, 2)
	if interactive.Streams != 1 || interactive.WindowChunks != profile.InteractiveWindowChunks || !interactive.CacheWrites {
		t.Fatalf("interactive plan = %+v", interactive)
	}

	probe := PlanResolver{Profile: profile}.Resolve(ReadIntentProbe, 16<<20, 2)
	if probe.Streams != 1 || probe.WindowChunks != profile.ProbeWindowChunks || probe.PrefetchDepth != 0 || probe.CacheWrites {
		t.Fatalf("probe plan = %+v", probe)
	}
}

type planCaptureHost struct {
	entry       drive.Entry
	data        []byte
	cacheKey    string
	plan        ReadPlan
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
	if host.plan.Intent != ReadIntentBulk || host.plan.Streams != 4 || host.plan.CacheWrites {
		t.Fatalf("read plan = %+v, want bulk/4/no-cache", host.plan)
	}
	if !host.prioritySet || host.priority != PriorityLow {
		t.Fatalf("priority = %v (set=%v), want low", host.priority, host.prioritySet)
	}
}

func TestApplyIntentPriority(t *testing.T) {
	bulk := applyIntentPriority(context.Background(), ReadPlan{Intent: ReadIntentBulk})
	if !prioritySet(bulk) || priority(bulk) != PriorityLow {
		t.Fatalf("bulk priority = %v, want low", priority(bulk))
	}
	interactive := applyIntentPriority(context.Background(), ReadPlan{Intent: ReadIntentInteractive})
	if !prioritySet(interactive) || priority(interactive) != PriorityHigh {
		t.Fatalf("interactive priority = %v, want high", priority(interactive))
	}
	explicit := applyIntentPriority(WithPriority(context.Background(), PriorityNormal), ReadPlan{Intent: ReadIntentBulk})
	if !prioritySet(explicit) || priority(explicit) != PriorityNormal {
		t.Fatalf("explicit priority = %v, want normal", priority(explicit))
	}
	unspecified := applyIntentPriority(context.Background(), ReadPlan{})
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
	if !host.ok || host.plan.Streams != 2 {
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
	if host.plan.Intent != ReadIntentInteractive || host.plan.Streams != 1 || !host.plan.CacheWrites {
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
	if host.plan.Intent != ReadIntentBulk || host.plan.Streams != 4 || !host.plan.CacheWrites {
		t.Fatalf("adaptive plan = %+v, want bulk/4/cache", host.plan)
	}
	if host.plan.WindowChunks != 1 {
		t.Fatalf("adaptive window = %d, want the FUSE window 1", host.plan.WindowChunks)
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
