package read

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// gatedWindowHost holds a foreground sequential window body after its first
// block, so a test can prove the head block is served while the tail is still
// in flight. Prefetch windows pass through untouched.
type gatedWindowHost struct {
	*stagedHost
	head, tail chan struct{}
	opened     chan struct{}
	openOnce   sync.Once
}

func (h *gatedWindowHost) DriverRead(ctx context.Context, e drive.Entry, offset, size int64) (io.ReadCloser, error) {
	rc, err := h.stagedHost.DriverRead(ctx, e, offset, size)
	if err != nil {
		return rc, err
	}
	if isPrefetchRead(ctx) || size != 2*ChunkSize {
		return rc, nil
	}
	h.openOnce.Do(func() { close(h.opened) })
	return &progressiveTestBody{ctx: ctx, ReadCloser: rc, head: h.head, tail: h.tail, remaining: ChunkSize}, nil
}

// TestForegroundWindowServesHeadBlockBeforeTail: with progressive publication a
// blocking foreground window must hand the awaited block to the reader before
// the rest of the window body arrives.
func TestForegroundWindowServesHeadBlockBeforeTail(t *testing.T) {
	r := newStagedReader(t, 16<<20)
	head, tail := make(chan struct{}), make(chan struct{})
	var headOnce, tailOnce sync.Once
	releaseHead := func() { headOnce.Do(func() { close(head) }) }
	releaseTail := func() { tailOnce.Do(func() { close(tail) }) }
	defer releaseHead()
	defer releaseTail()

	h := &gatedWindowHost{stagedHost: r.host, head: head, tail: tail, opened: make(chan struct{})}
	profile := drive.DefaultReadPolicy()
	profile.Interactive.WindowBytes = 2 * ChunkSize
	source, err := drive.NewReadSource(hostReadDriver{host: h, policy: profile}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	r.reader = NewReader(ReaderDeps{Host: h, State: r.state, Source: source})

	// Seed the sequential stream; the initial access stays a caller-sized
	// window, so this read does not touch the gated body.
	if got := r.read(t, WithoutReadPrefetch(sessionContext(1, 1)), 0, ChunkSize); !bytes.Equal(got, r.host.data[:ChunkSize]) {
		t.Fatal("initial read content differs")
	}

	releaseHead()
	headRead := r.goRead(WithoutReadPrefetch(sessionContext(1, 2)), ChunkSize, ChunkSize)
	select {
	case got := <-headRead:
		if got.err != nil {
			t.Fatalf("head read: %v", got.err)
		}
		if !bytes.Equal(got.data, r.host.data[ChunkSize:2*ChunkSize]) {
			t.Fatal("head block content differs")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("foreground read waited for the window tail; calls=%+v", r.host.recorded())
	}
	select {
	case <-h.opened:
	default:
		t.Fatal("window fetch was not in flight")
	}

	// The tail block is only readable once the provider delivers it.
	releaseTail()
	if got := r.read(t, WithoutReadPrefetch(sessionContext(1, 3)), 2*ChunkSize, ChunkSize); !bytes.Equal(got, r.host.data[2*ChunkSize:3*ChunkSize]) {
		t.Fatal("tail block content differs")
	}
}
