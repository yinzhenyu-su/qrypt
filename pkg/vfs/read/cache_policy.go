package read

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// CachePolicy is the single admission point for read chunks. Hot chunks are
// always kept (bounded, in-memory, and needed by the current read); the
// durable cache is written only when the resolved plan allows it.
type CachePolicy struct {
	hot     *State
	durable Cache
}

func newCachePolicy(state *State) *CachePolicy {
	if state == nil {
		return &CachePolicy{}
	}
	return &CachePolicy{hot: state, durable: state.cache}
}

// Admit records one fetched chunk. A missing plan keeps the pre-intent
// behavior (durable writes enabled) for direct callers such as tests.
func (p *CachePolicy) Admit(ctx context.Context, entry drive.Entry, cacheKey string, index int64, chunk []byte) {
	if p == nil || cacheKey == "" {
		return
	}
	if p.hot != nil {
		p.hot.putHotChunk(cacheKey, index, chunk)
	}
	if plan, ok := readPlanFromContext(ctx); ok && !plan.CacheWrites {
		return
	}
	if p.durable != nil {
		p.durable.PutChunkAsync(cacheKey, entry.Size, index, chunk, cacheAccess(ctx))
	}
}
