// Package mount adapts qrypt's platform-independent VFS API to OS mount
// backends.
//
// It owns FUSE callback translation, handle tracking, mount lifecycle, and
// platform compatibility behavior. Filesystem semantics should remain in
// pkg/vfs rather than in this package.
package mount
