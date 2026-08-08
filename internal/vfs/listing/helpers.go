package listing

import "context"

type dirPrefetchContextKey struct{}

// WithoutDirPrefetch disables background directory prefetch for ctx.
func WithoutDirPrefetch(ctx context.Context) context.Context {
	return context.WithValue(ctx, dirPrefetchContextKey{}, true)
}

// DirPrefetchEnabled reports whether directory prefetch is enabled.
func DirPrefetchEnabled(ctx context.Context) bool {
	disabled, _ := ctx.Value(dirPrefetchContextKey{}).(bool)
	return !disabled
}
