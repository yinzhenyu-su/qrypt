package drive

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

// TestFetchStreamSinglePartPublishesBlocksAsTheyArrive: FetchStream delivers one
// streaming Range, so blocks reach the consumer without waiting for the window
// body to finish. Part fan-out is the read layer job (FetchPart).
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
