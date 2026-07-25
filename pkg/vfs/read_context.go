package vfs

import "context"

type readPrefetchContextKey struct{}

func WithoutReadPrefetch(ctx context.Context) context.Context {
	return context.WithValue(ctx, readPrefetchContextKey{}, true)
}

func readPrefetchEnabled(ctx context.Context) bool {
	disabled, _ := ctx.Value(readPrefetchContextKey{}).(bool)
	return !disabled
}

// ReadPriority controls how read slots are allocated under contention.
type ReadPriority int

const (
	PriorityLow    ReadPriority = iota // prefetch, anticipatory reads
	PriorityNormal                     // default user-initiated reads
	PriorityHigh                       // UI-critical reads (thumbnails, visible content)
)

type readPriorityKey struct{}

func WithReadPriority(ctx context.Context, p ReadPriority) context.Context {
	return context.WithValue(ctx, readPriorityKey{}, p)
}

func readPriority(ctx context.Context) ReadPriority {
	p, _ := ctx.Value(readPriorityKey{}).(ReadPriority)
	return p
}
