package simread

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
)

// body is the paced, delayed, gated and possibly truncated read stream handed
// back by Provider.Read.
type body struct {
	p    *Provider
	base io.ReadCloser
	ctx  context.Context
	rec  *Request

	// limit is the truncation limit, or -1 for the full range.
	limit int64

	served    int64
	gateDone  bool
	ttfbDone  bool
	stallDone bool

	closed atomic.Bool
	once   sync.Once
}

func (b *body) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.closed.Load() {
		return 0, io.EOF
	}
	if !b.gateDone {
		b.gateDone = true
		if b.p.cfg.Gate != nil {
			if gate := b.p.cfg.Gate(b.rec.Offset, b.rec.Size); gate != nil {
				select {
				case <-gate:
				case <-b.ctx.Done():
					return 0, b.ctx.Err()
				}
			}
		}
	}
	if !b.ttfbDone {
		b.ttfbDone = true
		if err := b.p.sleep(b.ctx, b.p.cfg.TTFB); err != nil {
			return 0, err
		}
	}
	if b.limit >= 0 && b.served >= b.limit {
		return 0, io.EOF
	}
	if stall := b.p.cfg.Stall; !b.stallDone && stall.For > 0 && b.served >= stall.AfterBytes {
		b.stallDone = true
		if err := b.p.sleep(b.ctx, stall.For); err != nil {
			return 0, err
		}
	}
	readLen := len(p)
	if readLen > PaceSliceBytes {
		readLen = PaceSliceBytes
	}
	if b.limit >= 0 {
		if remain := b.limit - b.served; int64(readLen) > remain {
			readLen = int(remain)
		}
	}
	if readLen == 0 {
		return 0, io.EOF
	}
	if err := b.p.pace(b.ctx, readLen, b.p.cfg.RateBytesPerSec); err != nil {
		return 0, err
	}
	n, err := b.base.Read(p[:readLen])
	if n > 0 {
		b.served += int64(n)
	}
	return n, err
}

func (b *body) Close() error {
	var err error
	b.once.Do(func() {
		b.closed.Store(true)
		if b.base != nil {
			err = b.base.Close()
		}
		b.p.finish(b.rec, b.served, err)
		b.p.release()
	})
	return err
}

// compile-time check that the simulated body is a read closer.
var _ io.ReadCloser = (*body)(nil)
