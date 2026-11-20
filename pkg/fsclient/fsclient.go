// Package fsclient implements vfs.FileSystem over the resident Core's control
// data plane. It is the CLI-side client for the single owner runtime: every
// call becomes a command-endpoint request, so the client shares the owner's
// overlay, pending projections and caches instead of opening a second VFS.
package fsclient

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// FS is a remote vfs.FileSystem backed by control.Client.
type FS struct {
	client *control.Client
}

// New wraps an established control client.
func New(client *control.Client) *FS {
	return &FS{client: client}
}

func (f *FS) Stat(ctx context.Context, path string) (drive.Entry, error) {
	entry, err := f.client.FSStat(ctx, path)
	if err != nil {
		return drive.Entry{}, err
	}
	return entryFromDTO(entry), nil
}

func (f *FS) List(ctx context.Context, path string) ([]drive.Entry, error) {
	entries, err := f.client.FSList(ctx, path)
	if err != nil {
		return nil, err
	}
	result := make([]drive.Entry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entryFromDTO(entry))
	}
	return result, nil
}

func (f *FS) Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	data, err := f.client.FSRead(ctx, path, offset, size)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *FS) Create(ctx context.Context, path string) error {
	return f.client.FSCreate(ctx, path)
}

func (f *FS) WriteAt(ctx context.Context, path string, data []byte, off int64) (int, error) {
	return f.client.FSWriteAt(ctx, path, off, data)
}

func (f *FS) Flush(ctx context.Context, path string) error {
	return f.client.FSFlush(ctx, path)
}

func (f *FS) Mkdir(ctx context.Context, path string) (drive.Entry, error) {
	entry, err := f.client.FSMkdir(ctx, path)
	if err != nil {
		return drive.Entry{}, err
	}
	return entryFromDTO(entry), nil
}

func (f *FS) Remove(ctx context.Context, path string) error {
	return f.client.FSRemove(ctx, path, false)
}

func (f *FS) RemoveDir(ctx context.Context, path string) error {
	return f.client.FSRemove(ctx, path, true)
}

func (f *FS) Rename(ctx context.Context, oldPath, newPath string) error {
	return f.client.FSRename(ctx, oldPath, newPath)
}

func (f *FS) Truncate(ctx context.Context, path string, size int64) error {
	return f.client.FSTruncate(ctx, path, size)
}

// InvalidateListCache drops Core-side listings for path so the next List
// refetches from the backend (`fs list --refresh`).
func (f *FS) InvalidateListCache(ctx context.Context, path string) error {
	return f.client.FSInvalidateList(ctx, path)
}

// WaitPath reports when the Core has no pending work left for path.
func (f *FS) WaitPath(ctx context.Context, path string, timeout time.Duration) error {
	return f.client.FSWaitPath(ctx, path, timeout)
}

func entryFromDTO(entry control.FSEntry) drive.Entry {
	return drive.Entry{
		ID:       entry.ID,
		ParentID: entry.ParentID,
		Name:     entry.Name,
		IsDir:    entry.IsDir,
		Size:     entry.Size,
		ModTime:  entry.ModTime,
	}
}

var _ = drive.Entry{}
