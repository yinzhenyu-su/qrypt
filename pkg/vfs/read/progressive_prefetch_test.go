package read

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type progressiveHost struct {
	*stagedHost
	head, tail <-chan struct{}
	opened     chan struct{}
}

func (h progressiveHost) DriverRead(ctx context.Context, e drive.Entry, offset, size int64) (io.ReadCloser, error) {
	rc, err := h.stagedHost.DriverRead(ctx, e, offset, size)
	if err != nil || !isPrefetchRead(ctx) {
		return rc, err
	}
	close(h.opened)
	return &progressiveTestBody{ctx: ctx, ReadCloser: rc, head: h.head, tail: h.tail, remaining: ChunkSize}, nil
}

type progressiveTestBody struct {
	io.ReadCloser
	ctx        context.Context
	head, tail <-chan struct{}
	remaining  int
}

func (b *progressiveTestBody) Read(p []byte) (int, error) {
	gate := b.tail
	if b.remaining > 0 {
		gate = b.head
		p = p[:min(len(p), b.remaining)]
	}
	select {
	case <-gate:
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= n
	return n, err
}

func TestForegroundConsumesPrefetchBlockWhileTailBlocked(t *testing.T) {
	r := newStagedReader(t, 16<<20)
	head, tail := make(chan struct{}), make(chan struct{})
	defer close(tail)
	h := progressiveHost{stagedHost: r.host, head: head, tail: tail, opened: make(chan struct{})}
	p := drive.DefaultReadPolicy()
	p.MaxInFlight = 1
	source, err := drive.NewReadSource(hostReadDriver{host: h, policy: p}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	r.reader = NewReader(ReaderDeps{Host: h, State: r.state, Source: source})
	const target = 3 * ChunkSize
	r.reader.prefetchWindow(sessionContext(99, 1), r.host.entry, target/ChunkSize, 16)
	select {
	case <-h.opened:
	case <-time.After(time.Second):
		t.Fatal("prefetch did not open")
	}
	first := r.goRead(WithoutReadPrefetch(sessionContext(1, 2)), target, ChunkSize)
	second := r.goRead(WithoutReadPrefetch(sessionContext(2, 1)), target, ChunkSize)
	deadline := time.After(2 * time.Second)
	// Both readers must attach before the first block is released; neither
	// cache hits nor a completed window can make this test pass accidentally.
	for {
		r.state.windows.mu.Lock()
		waiters := 0
		for _, load := range r.state.windows.loads {
			waiters += load.waiters
		}
		r.state.windows.mu.Unlock()
		if waiters == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("foreground readers did not join live prefix")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(head)
	for _, result := range []<-chan asyncRead{first, second} {
		select {
		case got := <-result:
			if got.err != nil || !bytes.Equal(got.data, r.host.data[target:target+ChunkSize]) {
				t.Fatalf("bad prefix: %v", got.err)
			}
		case <-time.After(time.Second):
			t.Fatal("foreground blocked on speculative tail")
		}
	}
	if len(r.host.recorded()) != 1 {
		t.Fatalf("duplicate foreground request: %+v", r.host.recorded())
	}
	if source.Scheduler().PerFileInUse(r.host.entry.ID) != 1 {
		t.Fatal("tail is no longer in flight")
	}
}
