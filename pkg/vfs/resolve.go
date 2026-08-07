package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"strings"
)

// splitVirtual splits a cleaned virtual path into the last component (name)
// and its parent directory. Unlike filepath.Dir/Base, this uses forward-slash
// semantics regardless of the host OS, which is required for virtual FUSE paths.
func splitVirtual(p string) (name, parent string) {
	if p == "/" {
		return "/", "/"
	}
	idx := strings.LastIndexByte(p, '/')
	if idx <= 0 {
		return p[1:], "/"
	}
	return p[idx+1:], p[:idx]
}

func joinVirtual(parent, name string) string {
	parent = cleanVirtual(parent)
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

func isPathUnder(path, dir string) bool {
	path = cleanVirtual(path)
	dir = cleanVirtual(dir)
	return dir != "/" && strings.HasPrefix(path, dir+"/")
}

func (v *VFS) parent(ctx context.Context, path string) (drive.Entry, string, error) {
	path = cleanVirtual(path)
	name, parentPath := splitVirtual(path)
	parent, err := v.resolve(ctx, parentPath)
	return parent, name, err
}

func (v *VFS) resolve(ctx context.Context, path string) (drive.Entry, error) {
	runtime := newVFSResolveRuntime(v)
	return resolveWithRuntime(ctx, path, runtime, v.resolve, v.listChildren)
}

func resolveWithRuntime(ctx context.Context, path string, runtime resolveRuntime, resolveParent func(context.Context, string) (drive.Entry, error), listChildren func(context.Context, string, string) ([]drive.Entry, error)) (drive.Entry, error) {
	path = cleanVirtual(path)
	if runtime.IsUnavailable(path) {
		return drive.Entry{}, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if entry, ok := runtime.CachedEntry(path); ok {
		return entry, nil
	}
	name, parentPath := splitVirtual(path)
	parent, err := resolveParent(ctx, parentPath)
	if err != nil {
		return drive.Entry{}, err
	}
	if runtime.IsRecentLocalDir(parentPath) {
		return drive.Entry{}, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	entries, err := listChildren(ctx, parentPath, parent.ID)
	if err != nil {
		return drive.Entry{}, err
	}
	if child, ok := runtime.CommitResolvedChildren(parentPath, name, entries); ok {
		return child, nil
	}
	return drive.Entry{}, fmt.Errorf("%w: %s", ErrNotFound, path)
}

type resolveRuntime interface {
	CachedEntry(path string) (drive.Entry, bool)
	CommitResolvedChildren(parentPath, name string, entries []drive.Entry) (drive.Entry, bool)
	IsUnavailable(path string) bool
	IsRecentLocalDir(path string) bool
}

type vfsResolveRuntime struct {
	v *VFS
}

func newVFSResolveRuntime(v *VFS) vfsResolveRuntime {
	return vfsResolveRuntime{v: v}
}

func (r vfsResolveRuntime) CachedEntry(path string) (drive.Entry, bool) {
	path = cleanVirtual(path)
	return r.v.view.entries.Get(path)
}

func (r vfsResolveRuntime) CommitResolvedChildren(parentPath, name string, entries []drive.Entry) (drive.Entry, bool) {
	parentPath = cleanVirtual(parentPath)
	var found drive.Entry
	var foundOK bool
	for _, child := range entries {
		childPath := joinVirtual(parentPath, child.Name)
		r.v.view.mu.RLock()
		child = r.v.applyLocalModTimeLocked(childPath, child)
		r.v.view.mu.RUnlock()
		r.v.view.entries.Set(childPath, child)
		if child.Name == name {
			found = child
			foundOK = true
		}
	}
	return found, foundOK
}

func (r vfsResolveRuntime) IsUnavailable(path string) bool {
	return r.v.isUnavailable(path)
}

func (r vfsResolveRuntime) IsRecentLocalDir(path string) bool {
	return r.v.isRecentLocalDir(path)
}
