package drive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type gatedChunkBody struct {
	ctx        context.Context
	head, tail *bytes.Reader
	release    <-chan struct{}
}

func (b *gatedChunkBody) Read(p []byte) (int, error) {
	if b.head.Len() > 0 {
		return b.head.Read(p)
	}
	select {
	case <-b.release:
		return b.tail.Read(p)
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
}
func (b *gatedChunkBody) Close() error { return nil }

func TestPendingReadDeliversPrefixBeforeTailWithOnePermit(t *testing.T) {
	data := bytes.Repeat([]byte("p"), 3*ReadChunkSize)
	s := newPolicySource(t, data, 1)
	e := Entry{ID: "0/file.bin", Size: int64(len(data))}
	release := make(chan struct{})
	ctx := WithReadBodyTransform(context.Background(), func(ctx context.Context, _ Entry, _, _ int64, rc io.ReadCloser) (io.ReadCloser, error) {
		rc.Close()
		return &gatedChunkBody{ctx: ctx, head: bytes.NewReader(data[:ReadChunkSize]), tail: bytes.NewReader(data[ReadChunkSize:]), release: release}, nil
	})
	job := s.TryFetch(e, RangePlan{Size: int64(len(data))})
	if job == nil {
		t.Fatal("admission failed")
	}
	defer job.Close()
	chunks := make(chan []byte, 3)
	done := make(chan error, 1)
	go func() { done <- job.RunChunks(ctx, func(_ int64, b []byte) { chunks <- b }) }()
	select {
	case b := <-chunks:
		if !bytes.Equal(b, data[:ReadChunkSize]) {
			t.Fatal("wrong prefix")
		}
	case <-time.After(time.Second):
		t.Fatal("prefix blocked behind tail")
	}
	select {
	case err := <-done:
		t.Fatalf("tail completed before release: %v", err)
	default:
	}
	if s.scheduler.PerFileInUse(e.ID) != 1 {
		t.Fatal("stream released its permit before tail")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.scheduler.PerFileInUse(e.ID) != 0 {
		t.Fatal("completed job retained admission")
	}
	if len(chunks) != 2 {
		t.Fatalf("tail chunks=%d", len(chunks))
	}
}

type closeErrorChunkBody struct {
	io.Reader
	err error
}

func (b closeErrorChunkBody) Close() error { return b.err }

func TestProgressiveRangeFailureBoundaries(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 3*ReadChunkSize+17)
	for _, scenario := range []string{"short", "empty-tail", "idle", "overlong", "close", "cancel", "total"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var offsets, sizes []int64
			var got []byte
			publishCount := 0
			delivery := &rangeDelivery{emit: func(offset int64, b []byte) {
				if offset != int64(len(got)) {
					t.Errorf("duplicate or missing delivery: %d vs %d", offset, len(got))
				}
				got = append(got, b...)
				publishCount++
				if scenario == "cancel" {
					cancel()
				}
			}}
			terminal := errors.New("close failure")
			open := func(attemptCtx context.Context, _ Entry, offset, size int64) (io.ReadCloser, error) {
				offsets = append(offsets, offset)
				sizes = append(sizes, size)
				if len(offsets) == 1 {
					switch scenario {
					case "short":
						return io.NopCloser(bytes.NewReader(data[:ReadChunkSize+123])), nil
					case "empty-tail":
						return io.NopCloser(bytes.NewReader(data[:ReadChunkSize])), nil
					case "idle", "total":
						return &gatedChunkBody{ctx: attemptCtx, head: bytes.NewReader(data[:ReadChunkSize]), tail: bytes.NewReader(nil), release: make(chan struct{})}, nil
					case "overlong":
						return io.NopCloser(bytes.NewReader(append(append([]byte{}, data...), 1))), nil
					case "close":
						return closeErrorChunkBody{Reader: bytes.NewReader(data), err: terminal}, nil
					}
				}
				return io.NopCloser(bytes.NewReader(data[offset : offset+size])), nil
			}
			// The bounds only have to separate the timeout kinds from the
			// classification under test; they are not what makes an attempt
			// slow. Keep them far from any plausible scheduling delay: a 20ms
			// idle limit tripped while a race-enabled CI runner drained an
			// in-memory body, which silently turned every scenario into an
			// idle-replay case before the intended path ran. Only the two
			// scenarios that wait for a bound to expire use the tighter
			// 300ms limit (15x the flake that bit us); the rest stay wide
			// because they never wait on the timer.
			bounds := FetchTimeouts{Idle: time.Second, Total: 15 * time.Second}
			switch scenario {
			case "idle":
				bounds = FetchTimeouts{Idle: 300 * time.Millisecond, Total: 15 * time.Second}
			case "total":
				// The gated body publishes its head immediately and then
				// stalls, so a wide total budget still expires during the
				// fetch while the prefix is already delivered.
				bounds = FetchTimeouts{Idle: 15 * time.Second, Total: 300 * time.Millisecond}
			}
			_, extra, err := fetchRange(ctx, Entry{Size: int64(len(data))}, 0, int64(len(data)), bounds, open, delivery)
			switch scenario {
			case "short", "empty-tail", "idle":
				if err != nil || !bytes.Equal(got, data) || len(offsets) != 2 || offsets[1] != ReadChunkSize || sizes[1] != int64(len(data)-ReadChunkSize) {
					t.Fatalf("resume failed: err=%v offsets=%v sizes=%v bytes=%d extra=%v", err, offsets, sizes, len(got), extra)
				}
				if publishCount != 4 {
					t.Fatalf("publish count=%d", publishCount)
				}
			default:
				if err == nil {
					t.Fatal("terminal failure accepted")
				}
				if len(offsets) != 1 {
					t.Fatalf("terminal failure retried: %v", offsets)
				}
				want := 3 * ReadChunkSize
				if scenario == "cancel" || scenario == "total" {
					want = ReadChunkSize
				}
				if len(got) != want {
					t.Fatalf("unsafe or missing prefix: %d want %d", len(got), want)
				}
				if scenario == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if scenario == "total" && !IsFetchTimeout(err) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestProgressiveRangeDoesNotPublishIncompleteTailAfterRetryLimit(t *testing.T) {
	data := bytes.Repeat([]byte("tail"), ReadChunkSize/2)
	var offsets []int64
	var got []byte
	delivery := &rangeDelivery{emit: func(_ int64, b []byte) { got = append(got, b...) }}
	_, extra, err := fetchRange(context.Background(), Entry{Size: int64(len(data))}, 0, int64(len(data)), FetchTimeouts{Idle: time.Second, Total: time.Second}, func(_ context.Context, _ Entry, off, size int64) (io.ReadCloser, error) {
		offsets = append(offsets, off)
		return io.NopCloser(bytes.NewReader(data[off : int64(len(data))-1])), nil
	}, delivery)
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(offsets) != 2 || offsets[1] != ReadChunkSize || len(got) != ReadChunkSize || extra["fetch_attempts"] != 2 {
		t.Fatalf("unbounded retry or unsafe tail: offsets=%v bytes=%d extra=%v err=%v", offsets, len(got), extra, err)
	}
}

func TestPendingReadUnknownSizeRemainsBuffered(t *testing.T) {
	data := bytes.Repeat([]byte("u"), ReadChunkSize+17)
	s := newPolicySource(t, data, 1)
	job := s.TryFetch(Entry{ID: "0/file.bin"}, RangePlan{Size: int64(len(data)) + 100})
	if job == nil {
		t.Fatal("admission failed")
	}
	defer job.Close()
	var got []byte
	err := job.RunChunks(context.Background(), func(off int64, b []byte) {
		if off != int64(len(got)) {
			t.Errorf("unexpected offset=%d", off)
		}
		got = append(got, b...)
	})
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("unknown-size result=%d err=%v", len(got), err)
	}
}
