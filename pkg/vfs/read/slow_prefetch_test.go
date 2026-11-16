package read

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type slowPrefetchHost struct {
	*stagedHost
	stallAfter int64
}

func (h slowPrefetchHost) DriverRead(ctx context.Context, e drive.Entry, offset, size int64) (io.ReadCloser, error) {
	rc, err := h.stagedHost.DriverRead(ctx, e, offset, size)
	if err != nil || !isPrefetchRead(ctx) {
		return rc, err
	}
	return &slowPrefetchBody{ReadCloser: rc, ctx: ctx, remaining: size, size: size, stallAfter: h.stallAfter}, nil
}

type slowPrefetchBody struct {
	io.ReadCloser
	ctx        context.Context
	remaining  int64
	size       int64
	stallAfter int64
}

func (b *slowPrefetchBody) Read(p []byte) (int, error) {
	if b.stallAfter > 0 && b.size-b.remaining >= b.stallAfter {
		<-b.ctx.Done()
		return 0, b.ctx.Err()
	}
	if b.remaining > 0 {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		}
		p = p[:min(len(p), 32<<10)]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

// Each 512 KiB block takes 3.2s, longer than takeover's no-progress budget.
// Both lookup and registration joins must keep the original progressing body,
// return the right target, and keep admission within budget one.
func TestSlowPrefetchProgressKeepsBothJoinPaths(t *testing.T) {
	for _, path := range []string{"lookup", "registration"} {
		t.Run(path, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newStagedReader(t, 8*ChunkSize)
				h := slowPrefetchHost{stagedHost: r.host}
				p := quarkLikeProfile()
				p.MaxInFlight = 1
				source, err := drive.NewReadSource(hostReadDriver{host: h, policy: p}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer source.Close()
				r.reader = NewReader(ReaderDeps{Host: h, State: r.state, Source: source})
				ctx := sessionContext(99, 1)
				count := 4
				if path == "registration" {
					count = 1 // Same shape; wider loads are trimmed before registration.
				}
				load := r.reader.prefetchWindow(ctx, r.host.entry, 3, count)
				if load == nil {
					t.Fatal("prefetch not admitted")
				}
				synctest.Wait()
				ctx = WithReadPlan(ctx, ReadPlan{Stage: StageSequential, MaxStreams: 1})
				started := time.Now()
				var data []byte
				if path == "lookup" {
					var ok bool
					data, ok, err = r.reader.waitWindow(ctx, r.host.cacheKey, 3)
					if !ok {
						t.Fatal("progressing target was bypassed")
					}
				} else {
					data, err = r.reader.loadWindow(ctx, r.host.entry, 3, count)
				}
				if err != nil || !bytes.Equal(data, r.host.data[3*ChunkSize:4*ChunkSize]) {
					t.Fatalf("wrong target: %v", err)
				}
				if elapsed := time.Since(started); elapsed != 3200*time.Millisecond {
					t.Fatalf("target delivery took %s, want 3.2s without waiting for tail", elapsed)
				}
				if r.host.callCount() != 1 || r.host.maxConcurrent() > 1 {
					t.Fatal("target was refetched or exceeded budget")
				}
				if path == "lookup" && source.Scheduler().PerFileInUse(r.host.entry.ID) != 1 {
					t.Fatal("unread tail lost its permit")
				}
			})
		})
	}
}

func TestPrefetchTargetWaitKeepsBounds(t *testing.T) {
	for _, kind := range []string{"stall", "stall_after_progress", "distant", "unknown", "caller", "total"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newStagedReader(t, 8*ChunkSize)
				if kind == "unknown" {
					r.host.entry.Size = 0 // Buffered fallback cannot publish a prefix.
				}
				h := slowPrefetchHost{stagedHost: r.host}
				if kind == "stall_after_progress" {
					h.stallAfter = 32 << 10
				}
				p := quarkLikeProfile()
				p.MaxInFlight = 1
				if kind == "total" {
					p.Fetch.Total = 2500 * time.Millisecond
					p.Fetch.Idle = time.Second
				}
				source, err := drive.NewReadSource(hostReadDriver{host: h, policy: p}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer source.Close()
				r.reader = NewReader(ReaderDeps{Host: h, State: r.state, Source: source})
				if kind == "stall" {
					r.host.gateFn = func(_, _ int64) (<-chan struct{}, bool) { return make(chan struct{}), true }
				}
				load := r.reader.prefetchWindow(sessionContext(99, 1), r.host.entry, 3, 4)
				if load == nil {
					t.Fatal("prefetch not admitted")
				}
				synctest.Wait()
				ctx := context.Background()
				if kind == "caller" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, time.Second)
					defer cancel()
				}
				index := int64(3)
				if kind == "distant" {
					index = 6 // Earlier prefix progress cannot renew this target.
				}
				started := time.Now()
				ready, err := load.waitTarget(ctx, index)
				switch kind {
				case "caller":
					if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != time.Second {
						t.Fatalf("caller deadline ignored: %v", err)
					}
				case "total":
					if !ready || err != nil || time.Since(started) != p.Fetch.Total {
						t.Fatalf("shared total deadline ignored: %t, %v", ready, err)
					}
					if _, err := load.result(index); !drive.IsFetchTimeout(err) {
						t.Fatalf("missing terminal fetch error: %v", err)
					}
				default:
					wait := prefetchWaitBudget
					if kind == "stall_after_progress" {
						wait += 200 * time.Millisecond
					}
					if ready || err != nil || time.Since(started) != wait {
						t.Fatalf("takeover bound lost: %t, %v", ready, err)
					}
				}
			})
		})
	}
}
