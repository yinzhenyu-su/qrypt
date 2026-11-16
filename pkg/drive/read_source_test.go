package drive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/readbudget"
)

func newPolicySource(t *testing.T, data []byte, budget int) *ReadSource {
	t.Helper()
	d := seededDriver(t, data, CapabilityParallelRange)
	source, err := NewReadSource(d, &ReadOverride{MaxInFlight: &budget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Error(err)
		}
	})
	return source
}

func TestSourceTraceReportsProgressBeforeWindowCompletes(t *testing.T) {
	s := newPolicySource(t, bytes.Repeat([]byte("x"), 2*ReadChunkSize), 1)
	trace := &ReadTrace{}
	ctx := WithReadTrace(context.Background(), trace)
	body, err := s.OpenRange(ctx, Entry{ID: "0/file.bin", Size: 2 * ReadChunkSize}, 0, 2*ReadChunkSize, readbudget.PriorityNormal, false)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	buf := make([]byte, ReadChunkSize)
	if n, err := io.ReadFull(body, buf); n != ReadChunkSize || err != nil {
		t.Fatalf("partial read: %d, %v", n, err)
	}
	progress := trace.Snapshot()
	if progress["body_bytes"] != int64(ReadChunkSize) {
		t.Fatalf("incomplete body progress: %v", progress)
	}
	if _, ok := progress["first_body_us"].(int64); !ok {
		t.Fatalf("missing first body timing: %v", progress)
	}
	if _, ok := progress["first_chunk_us"].(int64); !ok {
		t.Fatalf("missing first chunk timing before completion: %v", progress)
	}
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	if progress := trace.Snapshot(); progress["body_bytes"] != int64(2*ReadChunkSize) {
		t.Fatalf("completed body progress: %v", progress)
	}
}
func TestSourceBodyAndFetchShareObjectBudget(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 1<<20)
	s := newPolicySource(t, data, 1)
	e := Entry{ID: "0/file.bin", Size: int64(len(data))}
	body, err := s.OpenRange(context.Background(), e, 0, 64, readbudget.PriorityNormal, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.ranges.Load(); got != 1 {
		t.Fatalf("source in-flight = %d, want 1", got)
	}
	if b, err := s.OpenRange(context.Background(), e, 64, 64, readbudget.PriorityLow, true); !errors.Is(err, ErrReadAdmissionBusy) {
		if b != nil {
			b.Close()
		}
		t.Fatalf("speculative admission: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := s.Fetch(ctx, e, RangePlan{Size: 128, Streams: 3}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fetch bypassed open body budget: %v", err)
	}
	body.Close()
	body.Close()
	if got := s.ranges.Load(); got != 0 {
		t.Fatalf("source in-flight after repeated close = %d, want 0", got)
	}
	got, err := s.Fetch(context.Background(), e, RangePlan{Offset: 17, Size: 121, Streams: 3})
	if err != nil || !bytes.Equal(got, data[17:138]) {
		t.Fatalf("fetch after release: %d/%v", len(got), err)
	}
	if got := s.ranges.Load(); got != 0 {
		t.Fatalf("source in-flight after fetch = %d, want 0", got)
	}
}

// A window row must state its traffic even when the body never started: a
// missing count is indistinguishable from an unobserved one, and reconcilers
// treat the two very differently.
func TestTraceSnapshotPublishesZeroTrafficAndPrefix(t *testing.T) {
	trace := &ReadTrace{}
	snapshot := trace.Snapshot()
	if got, ok := snapshot["body_bytes"].(int64); !ok || got != 0 {
		t.Fatalf("quiet trace traffic = %v, want 0", snapshot["body_bytes"])
	}
	if _, ok := snapshot["progress_end"]; ok {
		t.Fatalf("quiet trace reported a prefix: %v", snapshot)
	}
	trace.bodyProgress(4096, 1024, 4096, time.Now())
	snapshot = trace.Snapshot()
	if got := snapshot["body_bytes"]; got != int64(4096) {
		t.Fatalf("traffic = %v, want 4096", got)
	}
	if got := snapshot["progress_end"]; got != int64(1024+4096) {
		t.Fatalf("prefix = %v, want %d", got, 1024+4096)
	}
}

func TestSourceRangeEventsSpanIndependentTraces(t *testing.T) {
	s := newPolicySource(t, []byte("content"), 2)
	var events []ReadRangeEvent
	observe := func(event ReadRangeEvent) { events = append(events, event) }
	e := Entry{ID: "0/file.bin", Size: 7}
	first, err := s.OpenRange(WithReadTrace(context.Background(), &ReadTrace{ObserveRange: observe}), e, 0, 1, readbudget.PriorityNormal, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := s.OpenRange(WithReadTrace(context.Background(), &ReadTrace{ObserveRange: observe}), e, 1, 1, readbudget.PriorityNormal, false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := io.ReadAll(first); err != nil {
		t.Fatal(err)
	}
	first.Close()
	second.Close()
	if len(events) != 4 {
		t.Fatalf("range events = %+v", events)
	}
	for i, want := range []int32{1, 2, 1, 0} {
		if events[i].InFlight != want || events[i].Finished != (i >= 2) || events[i].SourceID != s.Policy().SourceID {
			t.Fatalf("range event %d = %+v", i, events[i])
		}
	}
	if events[0].ID == events[1].ID || events[0].ID != events[2].ID || events[1].ID != events[3].ID {
		t.Fatalf("range identities = %+v", events)
	}
}
func TestSourceCloseClosesOutstandingBodies(t *testing.T) {
	s := newPolicySource(t, bytes.Repeat([]byte("a"), 1024), 1)
	e := Entry{ID: "0/file.bin", Size: 1024}
	b, err := s.OpenRange(context.Background(), e, 0, 16, readbudget.PriorityNormal, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b.Close()
	if _, err := s.OpenRange(context.Background(), e, 0, 16, readbudget.PriorityNormal, false); !errors.Is(err, ErrReadSourceClosed) {
		t.Fatalf("closed source reopened: %v", err)
	}
}
func TestFixedSplitCeilingIsIndependentOfWorkers(t *testing.T) {
	layout := partitionRange(7, 24<<20, 3, SplitPolicy{PartBytes: 4 << 20})
	spans := make([]readRange, layout.count())
	for i := range spans {
		spans[i] = layout.at(int64(i))
	}
	if len(spans) != 6 {
		t.Fatalf("tasks = %d, want 6", len(spans))
	}
	end := int64(7)
	for _, span := range spans {
		if span.Offset != end || span.Size > 4<<20 {
			t.Fatalf("invalid shape: %+v", spans)
		}
		end += span.Size
	}
	if end != 7+24<<20 {
		t.Fatalf("wrong coverage: %d", end)
	}
}
func TestSourceStreamRefillsBoundedWindows(t *testing.T) {
	data := bytes.Repeat([]byte("window-boundary"), 1000000)
	s := newPolicySource(t, data, 3)
	e := Entry{ID: "0/file.bin", Size: int64(len(data))}
	body, err := s.Read(context.Background(), e, 177, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(got, data[177:]) {
		t.Fatalf("wrong stream: %d/%v", len(got), err)
	}
	d := s.driver.(*countingReadDriver)
	for _, call := range d.ReadCalls() {
		if call.Size > 8<<20 {
			t.Fatalf("unbounded range: %+v", call)
		}
	}
}

func TestSourceNaturalCompletionReleasesAdmission(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 256)
	s := newPolicySource(t, data, 1)
	e := Entry{ID: "0/file.bin", Size: int64(len(data))}
	body, err := s.OpenRange(context.Background(), e, 0, 32, readbudget.PriorityNormal, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	if s.scheduler.PerFileInUse(e.ID) != 0 {
		t.Fatal("natural completion retained admission")
	}
	if _, err := s.Fetch(context.Background(), e, RangePlan{Offset: 32, Size: 32, Streams: 1}); err != nil {
		t.Fatal(err)
	}
	body.Close()
	body.Close()
}

// HTTP response bodies reject reads after Close. Validation must read once
// more for EOF before the body is closed and its admission is released.
type closeSensitiveBody struct {
	mu     sync.Mutex
	reader *bytes.Reader
	closed bool
}

func (b *closeSensitiveBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	return b.reader.Read(p)
}

func (b *closeSensitiveBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func TestSourceFetchValidatesBeforeClosingCompletedBody(t *testing.T) {
	for _, delta := range []int{0, 1, -1} {
		t.Run(fmt.Sprintf("length_delta_%d", delta), func(t *testing.T) {
			data := bytes.Repeat([]byte("x"), 1<<20)
			s := newPolicySource(t, data, 1)
			d := s.driver.(*countingReadDriver)
			d.ReadBehavior.BodyFunc = func(_ context.Context, data []byte) io.ReadCloser {
				return &closeSensitiveBody{reader: bytes.NewReader(bytes.Repeat([]byte("x"), len(data)+delta))}
			}
			e := Entry{ID: "0/file.bin", Size: int64(len(data))}
			got, err := s.Fetch(context.Background(), e, RangePlan{Size: e.Size, Streams: 1})
			switch delta {
			case 0:
				if err != nil || !bytes.Equal(got, data) || len(d.ReadCalls()) != 1 {
					t.Fatalf("completed body: bytes=%d calls=%d err=%v", len(got), len(d.ReadCalls()), err)
				}
			case 1:
				if err == nil || !strings.Contains(err.Error(), "more data than requested") {
					t.Fatalf("oversized body accepted: %v", err)
				}
			case -1:
				if !errors.Is(err, io.ErrUnexpectedEOF) || len(d.ReadCalls()) != 2 {
					t.Fatalf("truncation lost: calls=%d err=%v", len(d.ReadCalls()), err)
				}
			}
			if s.scheduler.PerFileInUse(e.ID) != 0 {
				t.Fatal("completed fetch retained admission")
			}
		})
	}
}
func TestPendingReadCancellationAndSourceClose(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 1024)
	d := seededDriver(t, data)
	budget := 1
	s, err := NewReadSource(d, &ReadOverride{MaxInFlight: &budget})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := Entry{ID: "0/file.bin", Size: int64(len(data))}
	p := RangePlan{Size: 1024, Priority: readbudget.PriorityLow}
	job := s.TryFetch(e, p)
	if job == nil {
		t.Fatal("idle speculative admission failed")
	}
	if next := s.TryFetch(e, p); next != nil {
		next.Close()
		t.Fatal("speculation exceeded budget")
	}
	job.Close()
	job.Close()
	if _, err := job.Run(context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("closed job ran: %v", err)
	}
	gate := make(chan struct{})
	d.ReadBehavior.GateFunc = func(int64, int64) <-chan struct{} { return gate }
	job = s.TryFetch(e, p)
	if job == nil {
		t.Fatal("unstarted job retained admission")
	}
	done := make(chan error, 1)
	go func() { _, err := job.Run(context.Background()); done <- err }()
	waitForReadActive(t, d.FakeDriver, 1)
	job.Close()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending read did not stop")
	}
	if s.scheduler.PerFileInUse(e.ID) != 0 {
		t.Fatal("canceled job retained admission")
	}
	job = s.TryFetch(e, p)
	if job == nil {
		t.Fatal("idle admission was not restored")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := job.Run(context.Background()); err == nil {
		t.Fatal("source close left pending work runnable")
	}
	if s.scheduler.PerFileInUse(e.ID) != 0 {
		t.Fatal("source close leaked reservation")
	}
}
func TestSourceFixedPartQueueCoversMoreTasksThanWorkers(t *testing.T) {
	data := bytes.Repeat([]byte("q"), 24<<20)
	d := seededDriver(t, data, CapabilityParallelRange)
	gate := make(chan struct{})
	d.ReadBehavior.GateFunc = func(int64, int64) <-chan struct{} { return gate }
	var once sync.Once
	defer once.Do(func() { close(gate) })
	s, err := NewReadSource(d, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := Entry{ID: "0/file.bin", Size: int64(len(data))}
	type outcome struct {
		data []byte
		err  error
	}
	done := make(chan outcome, 1)
	trace := &ReadTrace{}
	go func() {
		b, err := s.Fetch(WithReadTrace(context.Background(), trace), e, RangePlan{Size: int64(len(data)), Streams: 3, Split: SplitPolicy{PartBytes: 4 << 20}})
		done <- outcome{b, err}
	}()
	waitForReadActive(t, d.FakeDriver, 3)
	once.Do(func() { close(gate) })
	result := <-done
	if result.err != nil || !bytes.Equal(result.data, data) {
		t.Fatalf("result bytes=%d error=%v", len(result.data), result.err)
	}
	stats := trace.Snapshot()
	if stats["actual_in_flight"] != 3 || stats["range_tasks"] != 6 {
		t.Fatalf("execution counters = %+v", stats)
	}
	for _, key := range []string{"queue_us", "open_us", "body_us", "local_wait_us", "validate_us"} {
		if _, ok := stats[key].(int64); !ok {
			t.Fatalf("missing timing %s: %+v", key, stats)
		}
	}
	if calls := d.ReadCalls(); len(calls) != 6 {
		t.Fatalf("tasks=%d want 6", len(calls))
	} else {
		for _, call := range calls {
			if call.Size > 4<<20 {
				t.Fatalf("oversized part %+v", call)
			}
		}
	}
	if active := d.ReadStats().MaxConcurrent; active > 3 {
		t.Fatalf("active=%d want <=3", active)
	}
	layout := partitionRange(0, MaxReadWindowBytes, 3, SplitPolicy{PartBytes: 1})
	if layout.count() != MaxReadWindowBytes || layout.workers != 3 {
		t.Fatalf("lazy layout=%+v", layout)
	}
}

func TestSourceRejectsInvalidPlansBeforeAdmission(t *testing.T) {
	s := newPolicySource(t, []byte("data"), 1)
	e := Entry{ID: "0/file.bin", Size: 4}
	for _, plan := range []RangePlan{
		{Size: 1, Split: SplitPolicy{PartBytes: 1<<63 - 1}},
		{Size: 1, Split: SplitPolicy{MinParallelBytes: MaxReadWindowBytes + 1}},
		{Size: 1, Streams: 33},
		{Offset: -1, Size: 1},
		{Offset: 1<<63 - 1, Size: 1},
	} {
		if _, err := s.Fetch(context.Background(), e, plan); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("plan %+v: %v", plan, err)
		}
	}
	if calls := s.driver.(*countingReadDriver).ReadStats().Calls; calls != 0 {
		t.Fatalf("invalid plans opened %d requests", calls)
	}
}
func TestSourceRequiresStableIDAndRejectsClosedReads(t *testing.T) {
	s := newPolicySource(t, []byte("x"), 1)
	if _, err := s.Fetch(context.Background(), Entry{Size: 1}, RangePlan{Size: 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown ID=%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(context.Background(), Entry{ID: "0/file.bin", Size: 1}, 0, 1); !errors.Is(err, ErrReadSourceClosed) {
		t.Fatalf("closed Read=%v", err)
	}
}
