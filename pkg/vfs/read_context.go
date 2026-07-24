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
