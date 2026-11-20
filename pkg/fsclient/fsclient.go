// Package fsclient implements vfs.FileSystem over the resident Core's control
// data plane. It is the CLI-side client for the single owner runtime: every
// call becomes a command-endpoint request, so the client shares the owner's
// overlay, pending projections and caches instead of opening a second VFS.
package fsclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
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
		return drive.Entry{}, mapFSError(err)
	}
	return entryFromDTO(entry), nil
}

func (f *FS) List(ctx context.Context, path string) ([]drive.Entry, error) {
	entries, err := f.client.FSList(ctx, path)
	if err != nil {
		return nil, mapFSError(err)
	}
	result := make([]drive.Entry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entryFromDTO(entry))
	}
	return result, nil
}

func (f *FS) Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	stream, err := f.client.FSReadStream(ctx, path, offset, size, false)
	if err != nil {
		return nil, mapFSError(err)
	}
	return stream, nil
}

// ReadRaw streams undecrypted backend bytes for `fs cat --raw`.
func (f *FS) ReadRaw(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	stream, err := f.client.FSReadStream(ctx, path, offset, size, true)
	if err != nil {
		return nil, mapFSError(err)
	}
	return stream, nil
}

// Capabilities declares the optional surfaces this client implements so the
// CLI gates (`fs df`, `fs cat --raw`, `fs pending`) behave like they do on a
// local VFS.
func (f *FS) Capabilities() []vfs.Capability {
	return []vfs.Capability{
		vfs.CapabilityRawRead,
		vfs.CapabilitySpace,
		vfs.CapabilityMountSpace,
		vfs.CapabilityUploadInspection,
	}
}

// Space reports aggregate capacity of the owner filesystem.
func (f *FS) Space(ctx context.Context) (drive.Space, error) {
	space, err := f.client.FSSpace(ctx)
	if err != nil {
		return drive.Space{}, mapFSError(err)
	}
	return drive.Space{Total: space.Total, Free: space.Free}, nil
}

// MountSpaces reports per-mount capacity breakdown.
func (f *FS) MountSpaces(ctx context.Context) []vfs.MountSpace {
	mounts, err := f.client.FSMountSpaces(ctx)
	if err != nil {
		return []vfs.MountSpace{{Err: mapFSError(err)}}
	}
	result := make([]vfs.MountSpace, 0, len(mounts))
	for _, mount := range mounts {
		item := vfs.MountSpace{Name: mount.Name, Space: drive.Space{Total: mount.Total, Free: mount.Free}}
		if mount.Error != "" {
			item.Err = errors.New(mount.Error)
		}
		result = append(result, item)
	}
	return result
}

// PendingUploads lists uploads staged but not yet committed in the Core.
func (f *FS) PendingUploads() []vfs.PendingUpload {
	pending, err := f.client.FSPendingUploads(context.Background())
	if err != nil {
		return nil
	}
	return pending
}

// mapFSError translates transport failures into the sentinel errors the CLI
// already understands, so a Core-backed `fs stat` reports "not found" like a
// local VFS instead of a raw HTTP status.
func mapFSError(err error) error {
	var transport *control.TransportError
	if !errors.As(err, &transport) {
		return err
	}
	switch transport.Status {
	case 404:
		if strings.Contains(transport.Body, drive.ErrNotFound.Error()) {
			return errors.New(transport.Body)
		}
		return fmt.Errorf("%w: %s", drive.ErrNotFound, transport.Body)
	case 503:
		return fmt.Errorf("resident Core unavailable: %s", transport.Body)
	case 504:
		return fmt.Errorf("resident Core timeout: %s", transport.Body)
	default:
		return err
	}
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
