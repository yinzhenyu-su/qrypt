package drive

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/readbudget"
)

// TestFetchStreamPublishesHeadBlockBeforeTail: a parallel window must hand
// completed blocks to the consumer while the slow tail is still in flight.
func TestFetchStreamPublishesHeadBlockBeforeTail(t *testing.T) {
	data := bytes.Repeat([]byte("a"), ReadChunkSize)
	data = append(data, bytes.Repeat([]byte("b"), ReadChunkSize)...)
	driver := seededDriver(t, data, CapabilityParallelRange)
	gate := make(chan struct{})
	driver.ReadBehavior = ReadBehavior{BodyFunc: func(ctx context.Context, span []byte) io.ReadCloser {
		if len(span) > 0 && span[0] == 'b' {
			select {
			case <-gate:
			case <-ctx.Done():
				return io.NopCloser(nil)
			}
		}
		return io.NopCloser(bytes.NewReader(span))
	}}
	budget := 4
	source, err := NewReadSource(driver, &ReadOverride{MaxInFlight: &budget})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	plan := RangePlan{
		Offset:   0,
		Size:     int64(len(data)),
		Streams:  2,
		Split:    SplitPolicy{MinParallelBytes: ReadChunkSize, PartBytes: ReadChunkSize},
		Timeouts: DefaultReadPolicy().Fetch,
		Priority: readbudget.PriorityNormal,
	}
	type streamBlock struct {
		offset int64
		data   []byte
	}
	blocks := make(chan streamBlock, 4)
	done := make(chan error, 1)
	go func() {
		done <- source.FetchStream(context.Background(), entry, plan, func(offset int64, block []byte) {
			blocks <- streamBlock{offset: offset, data: append([]byte(nil), block...)}
		})
	}()
	select {
	case first := <-blocks:
		if first.offset != 0 {
			t.Fatalf("first block offset = %d, want 0", first.offset)
		}
		if !bytes.Equal(first.data, data[:ReadChunkSize]) {
			t.Fatal("head block content differs")
		}
	case err := <-done:
		t.Fatalf("FetchStream returned before publishing the head block: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("head block was not published while the tail was in flight")
	}
	select {
	case extra := <-blocks:
		t.Fatalf("tail block published before release: offset %d", extra.offset)
	case <-done:
		t.Fatal("FetchStream returned before the tail was released")
	case <-time.After(50 * time.Millisecond):
	}
	close(gate)
	select {
	case tail := <-blocks:
		if tail.offset != ReadChunkSize {
			t.Fatalf("tail block offset = %d, want %d", tail.offset, ReadChunkSize)
		}
		if !bytes.Equal(tail.data, data[ReadChunkSize:]) {
			t.Fatal("tail block content differs")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tail block was not published after release")
	}
	if err := <-done; err != nil {
		t.Fatalf("FetchStream: %v", err)
	}
}

// TestFetchStreamSinglePartPublishesBlocksAsTheyArrive: a one-stream window
// streams its blocks without waiting for the window body to finish.
func TestFetchStreamSinglePartPublishesBlocksAsTheyArrive(t *testing.T) {
	data := append(bytes.Repeat([]byte("x"), ReadChunkSize), bytes.Repeat([]byte("y"), ReadChunkSize)...)
	driver := seededDriver(t, data, CapabilityParallelRange)
	gate := make(chan struct{})
	driver.ReadBehavior = ReadBehavior{BodyFunc: func(_ context.Context, span []byte) io.ReadCloser {
		return &gatedBody{Reader: bytes.NewReader(span), gate: gate, gateAt: ReadChunkSize}
	}}
	source, err := NewReadSource(driver, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	plan := RangePlan{Offset: 0, Size: int64(len(data)), Streams: 1, Timeouts: DefaultReadPolicy().Fetch}
	var offsets []int64
	published := make(chan int64, 2)
	done := make(chan error, 1)
	go func() {
		done <- source.FetchStream(context.Background(), entry, plan, func(offset int64, _ []byte) {
			offsets = append(offsets, offset)
			published <- offset
		})
	}()
	select {
	case offset := <-published:
		if offset != 0 {
			t.Fatalf("first published offset = %d, want 0", offset)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no block published before the window body finished")
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("FetchStream: %v", err)
	}
	if len(offsets) != 2 || offsets[1] != ReadChunkSize {
		t.Fatalf("published offsets = %v, want [0 %d]", offsets, ReadChunkSize)
	}
}

// gatedBody blocks once the reader has consumed gateAt bytes until gate closes.
type gatedBody struct {
	*bytes.Reader
	gate    <-chan struct{}
	gateAt  int64
	blocked bool
}

func (b *gatedBody) Read(p []byte) (int, error) {
	consumed := int64(b.Size()) - int64(b.Len())
	if !b.blocked && consumed >= b.gateAt {
		b.blocked = true
		<-b.gate
	}
	return b.Reader.Read(p)
}

func (b *gatedBody) Close() error { return nil }
