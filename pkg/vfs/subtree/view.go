// Package subtree exposes a path prefix of one vfs.FileSystem as a standalone
// filesystem view. The resident core keeps a single namespace VFS; every FUSE
// session (the namespace root or one configured mount) is served by projecting
// that VFS through this view, so all clients share one set of caches, pending
// uploads and mutation paths.
package subtree

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// View projects paths below prefix onto the underlying filesystem. An empty
// prefix is the root view and passes every path through unchanged.
type View struct {
	fs     vfs.FileSystem
	prefix string
}

// New builds a view of mountName inside fs. An empty mountName is the root
// view. The name must not contain a slash; configured mount names never do.
func New(fs vfs.FileSystem, mountName string) (*View, error) {
	if fs == nil {
		return nil, errors.New("subtree: nil filesystem")
	}
	if mountName == "" {
		return &View{fs: fs}, nil
	}
	if strings.ContainsRune(mountName, '/') || strings.ContainsRune(mountName, '\\') {
		return nil, errors.New("subtree: mount name must not contain a path separator")
	}
	return &View{fs: fs, prefix: "/" + mountName}, nil
}

// fullPath maps a view path to a namespace path. path.Clean makes the result
// absolute, so ".." components clamp at the view root instead of escaping it.
func (v *View) fullPath(p string) string {
	clean := path.Clean("/" + p)
	if v.prefix == "" {
		return clean
	}
	if clean == "/" {
		return v.prefix
	}
	return v.prefix + clean
}

// viewPath maps a namespace path back into the view. It reports false when the
// path lies outside the view and therefore must not reach the kernel.
func (v *View) viewPath(full string) (string, bool) {
	if v.prefix == "" {
		return full, true
	}
	if full == v.prefix {
		return "/", true
	}
	if strings.HasPrefix(full, v.prefix+"/") {
		return full[len(v.prefix):], true
	}
	return "", false
}

// Stat implements vfs.Reader.
func (v *View) Stat(ctx context.Context, p string) (drive.Entry, error) {
	return v.fs.Stat(ctx, v.fullPath(p))
}

// List implements vfs.Reader.
func (v *View) List(ctx context.Context, p string) ([]drive.Entry, error) {
	return v.fs.List(ctx, v.fullPath(p))
}

// Read implements vfs.Reader.
func (v *View) Read(ctx context.Context, p string, offset, size int64) (io.ReadCloser, error) {
	return v.fs.Read(ctx, v.fullPath(p), offset, size)
}

// Create implements vfs.Writer.
func (v *View) Create(ctx context.Context, p string) error {
	return v.fs.Create(ctx, v.fullPath(p))
}

// WriteAt implements vfs.Writer.
func (v *View) WriteAt(ctx context.Context, p string, data []byte, off int64) (int, error) {
	return v.fs.WriteAt(ctx, v.fullPath(p), data, off)
}

// Flush implements vfs.Writer.
func (v *View) Flush(ctx context.Context, p string) error {
	return v.fs.Flush(ctx, v.fullPath(p))
}

// Mkdir implements vfs.Writer.
func (v *View) Mkdir(ctx context.Context, p string) (drive.Entry, error) {
	return v.fs.Mkdir(ctx, v.fullPath(p))
}

// Remove implements vfs.Writer.
func (v *View) Remove(ctx context.Context, p string) error {
	return v.fs.Remove(ctx, v.fullPath(p))
}

// RemoveDir implements vfs.Writer.
func (v *View) RemoveDir(ctx context.Context, p string) error {
	return v.fs.RemoveDir(ctx, v.fullPath(p))
}

// Rename implements vfs.Writer. Both paths must stay inside the view.
func (v *View) Rename(ctx context.Context, oldPath, newPath string) error {
	return v.fs.Rename(ctx, v.fullPath(oldPath), v.fullPath(newPath))
}

// Truncate implements vfs.Writer.
func (v *View) Truncate(ctx context.Context, p string, size int64) error {
	return v.fs.Truncate(ctx, v.fullPath(p), size)
}

// SubscribeInvalidations implements vfs.InvalidationSource. Paths outside the
// view are dropped; the view root is reported as "/" so FUSE invalidates the
// mount point itself.
func (v *View) SubscribeInvalidations(cb func(string)) func() {
	source, ok := v.fs.(vfs.InvalidationSource)
	if !ok || cb == nil {
		return func() {}
	}
	return source.SubscribeInvalidations(func(full string) {
		if mapped, ok := v.viewPath(full); ok {
			cb(mapped)
		}
	})
}

var _ vfs.FileSystem = (*View)(nil)
var _ vfs.InvalidationSource = (*View)(nil)
