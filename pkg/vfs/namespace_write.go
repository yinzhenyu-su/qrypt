package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
	"time"
)

func (n *Namespace) Create(ctx context.Context, path string) error {
	if isVirtualRootAppleMetadataPath(path) {
		return nil
	}
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return err
	}
	if root || rest == "/" {
		return ErrReadOnly
	}
	return mount.Create(ctx, rest)
}

// DiscardUpload drops a pending staging generation on the resolved mount
// without touching its remote entry.
func (n *Namespace) DiscardUpload(path string) error {
	if isVirtualRootAppleMetadataPath(path) {
		return nil
	}
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return err
	}
	if root || rest == "/" {
		return ErrReadOnly
	}
	return mount.DiscardUpload(rest)
}

func (n *Namespace) WriteAt(ctx context.Context, path string, data []byte, off int64) (int, error) {
	if isVirtualRootAppleMetadataPath(path) {
		return len(data), nil
	}
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
	if isVirtualRootAppleMetadataPath(path) {
		return nil
	}
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
	if isVirtualRootAppleMetadataPath(path) {
		return virtualRootAppleMetadataEntry(path), nil
	}
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
	if isVirtualRootAppleMetadataPath(path) {
		return nil
	}
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
	if isVirtualRootAppleMetadataPath(path) {
		return nil
	}
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
	if isVirtualRootAppleMetadataPath(oldPath) || isVirtualRootAppleMetadataPath(newPath) {
		return nil
	}
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
	if isVirtualRootAppleMetadataPath(path) {
		return nil
	}
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
	if isVirtualRootAppleMetadataPath(path) {
		return nil
	}
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
	if isVirtualRootAppleMetadataPath(path) {
		return nil
	}
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
	path = vfstypes.CleanVirtualPath(path)
	return path == "/"
}
