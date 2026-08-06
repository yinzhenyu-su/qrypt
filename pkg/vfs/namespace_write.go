package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"time"
)

func (n *Namespace) Create(ctx context.Context, path string) error {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return err
	}
	if root || rest == "/" {
		return ErrReadOnly
	}
	return mount.Create(ctx, rest)
}

func (n *Namespace) WriteAt(ctx context.Context, path string, data []byte, off int64) (int, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return 0, err
	}
	if root || rest == "/" {
		return 0, ErrReadOnly
	}
	return mount.WriteAt(ctx, rest, data, off)
}

func (n *Namespace) Flush(ctx context.Context, path string) error {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return err
	}
	if root || rest == "/" {
		return ErrReadOnly
	}
	return mount.Flush(ctx, rest)
}

func (n *Namespace) Mkdir(ctx context.Context, path string) (drive.Entry, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return drive.Entry{}, err
	}
	if root || rest == "/" {
		return drive.Entry{}, ErrReadOnly
	}
	return mount.Mkdir(ctx, rest)
}

func (n *Namespace) Remove(ctx context.Context, path string) error {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return err
	}
	if root || rest == "/" {
		return ErrReadOnly
	}
	return mount.Remove(ctx, rest)
}

func (n *Namespace) RemoveDir(ctx context.Context, path string) error {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return err
	}
	if root || rest == "/" {
		return ErrReadOnly
	}
	return mount.RemoveDir(ctx, rest)
}

func (n *Namespace) Rename(ctx context.Context, oldPath, newPath string) error {
	oldMount, oldRest, oldRoot, err := n.resolve(oldPath)
	if err != nil {
		return err
	}
	newMount, newRest, newRoot, err := n.resolve(newPath)
	if err != nil {
		return err
	}
	if oldRoot || newRoot || oldRest == "/" || newRest == "/" {
		return ErrReadOnly
	}
	if oldMount != newMount {
		return fmt.Errorf("%w: %s -> %s", ErrCrossMount, oldPath, newPath)
	}
	return oldMount.Rename(ctx, oldRest, newRest)
}

func (n *Namespace) Truncate(ctx context.Context, path string, size int64) error {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return err
	}
	if root || rest == "/" {
		return ErrReadOnly
	}
	return mount.Truncate(ctx, rest, size)
}

func (n *Namespace) SetModTime(ctx context.Context, path string, modTime time.Time) error {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return err
	}
	if root || rest == "/" {
		return ErrReadOnly
	}
	return mount.SetModTime(ctx, rest, modTime)
}

func (n *Namespace) PrepareDirectoryCopy(ctx context.Context, path string) error {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return err
	}
	if root || rest == "/" {
		return ErrReadOnly
	}
	return mount.PrepareDirectoryCopy(ctx, rest)
}

func (n *Namespace) IsReadOnlyPath(path string) bool {
	path = cleanVirtual(path)
	return path == "/"
}
