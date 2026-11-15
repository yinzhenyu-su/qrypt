package drive

import "context"

type strictRangeContextKey struct{}

// WithStrictRange marks a read as one sub-range of a parallel request. A
// driver that cannot guarantee a 206 response must return ErrUnsupported
// instead of silently serving the whole object; the caller then falls back to
// a single stream.
func WithStrictRange(ctx context.Context) context.Context {
	return context.WithValue(ctx, strictRangeContextKey{}, true)
}

// StrictRangeFromContext reports whether the read must honor range semantics.
func StrictRangeFromContext(ctx context.Context) bool {
	value, _ := ctx.Value(strictRangeContextKey{}).(bool)
	return value
}
