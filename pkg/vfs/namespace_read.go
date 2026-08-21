package vfs

import (
	"context"
	"errors"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"io"
	"strings"
)

func (n *Namespace) FlushReadCache() error {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var errs []error
	for _, fs := range n.mounts {
		if err := fs.FlushReadCache(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (n *Namespace) ClearReadCache() error {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var errs []error
	for _, fs := range n.mounts {
		if err := fs.ClearReadCache(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (n *Namespace) ClearReadCacheForMount(name string) error {
	name = cleanMountName(name)
	n.mu.RLock()
	mount, ok := n.mounts[name]
	n.mu.RUnlock()
	if !ok {
		return fmt.Errorf("vfs: unknown mount %q", name)
	}
	return mount.ClearReadCache()
}

func (n *Namespace) CloseReadCache() error {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var errs []error
	for _, fs := range n.mounts {
		if err := fs.CloseReadCache(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (n *Namespace) StartDirectoryPrefetch(ctx context.Context) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, fs := range n.mounts {
		fs.StartDirectoryPrefetch(ctx)
	}
}

func (n *Namespace) Stat(ctx context.Context, path string) (drive.Entry, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return drive.Entry{}, err
	}
	if root {
		return drive.Entry{ID: "/", Name: "/", IsDir: true, ModTime: n.createdAt, CreatedAt: n.createdAt, UpdatedAt: n.createdAt}, nil
	}
	if rest == "/" {
		name := strings.Trim(strings.TrimPrefix(cleanVirtual(path), "/"), "/")
		return drive.Entry{ID: "/" + name, ParentID: "/", Name: name, IsDir: true, ModTime: n.createdAt, CreatedAt: n.createdAt, UpdatedAt: n.createdAt}, nil
	}
	return mount.Stat(ctx, rest)
}

func (n *Namespace) List(ctx context.Context, path string) ([]drive.Entry, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return nil, err
	}
	if root {
		return n.rootEntries(), nil
	}
	return mount.List(ctx, rest)
}

func (n *Namespace) ListPage(ctx context.Context, path, cursor string, limit int) (ListPageResult, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return ListPageResult{}, err
	}
	if root {
		return paginateEntries(n.rootEntries(), cursor, limit), nil
	}
	return mount.ListPage(ctx, rest, cursor, limit)
}

func (n *Namespace) RemoteList(ctx context.Context, path string) ([]drive.Entry, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return nil, err
	}
	if root {
		return n.rootEntries(), nil
	}
	return mount.RemoteList(ctx, rest)
}

func (n *Namespace) Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return nil, err
	}
	if root {
		return nil, fmt.Errorf("vfs: cannot read namespace root")
	}
	return mount.Read(ctx, rest, offset, size)
}

func (n *Namespace) ReadRaw(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return nil, err
	}
	if root {
		return nil, fmt.Errorf("vfs: cannot read namespace root")
	}
	return mount.ReadRaw(ctx, rest, offset, size)
}

func (n *Namespace) RefreshPath(path string) {
	mount, rest, _, err := n.resolve(path)
	if err != nil || mount == nil {
		return
	}
	mount.RefreshPath(rest)
}
