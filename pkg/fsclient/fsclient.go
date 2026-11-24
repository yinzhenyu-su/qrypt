// Package fsclient implements vfs.FileSystem over the resident Core's control
// data plane. It is the CLI-side client for the single owner runtime: every
// call becomes a command-endpoint request, so the client shares the owner's
// overlay, pending projections and caches instead of opening a second VFS.
package fsclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
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

// mutationContext gives each mutating request an idempotency key unless the
// caller already chose one (for a deliberate retry). The Core executes a
// repeated key once and replays its response, so a transport-level retry can
// never duplicate a mutation.
func mutationContext(ctx context.Context) context.Context {
	if control.OperationIDFromContext(ctx) != "" {
		return ctx
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ctx
	}
	return control.WithOperationID(ctx, hex.EncodeToString(raw[:]))
}

// CopyDirect runs a direct backend-to-backend copy in the Core and returns the
// driver_copy result document for the CLI to render.
func (f *FS) CopyDirect(ctx context.Context, source, destination string, recursive, force bool) (json.RawMessage, error) {
	return f.client.FSCopy(mutationContext(ctx), control.FSCopyRequest{
		Source:      source,
		Destination: destination,
		Recursive:   recursive,
		Force:       force,
	})
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
	return f.client.FSCreate(mutationContext(ctx), path)
}

func (f *FS) WriteAt(ctx context.Context, path string, data []byte, off int64) (int, error) {
	return f.client.FSWriteAt(mutationContext(ctx), path, off, data)
}

func (f *FS) Flush(ctx context.Context, path string) error {
	return f.client.FSFlush(mutationContext(ctx), path)
}

// SetModTime stamps a VFS file's mtime through the owner data plane. It makes
// *FS satisfy vfs.ModTimeWriter so sync's convergence stamp works over the
// client path too.
func (f *FS) SetModTime(ctx context.Context, path string, modTime time.Time) error {
	return mapFSError(f.client.FSSetModTime(mutationContext(ctx), path, modTime))
}

func (f *FS) Mkdir(ctx context.Context, path string) (drive.Entry, error) {
	entry, err := f.client.FSMkdir(mutationContext(ctx), path)
	if err != nil {
		return drive.Entry{}, err
	}
	return entryFromDTO(entry), nil
}

func (f *FS) Remove(ctx context.Context, path string) error {
	return f.client.FSRemove(mutationContext(ctx), path, false)
}

func (f *FS) RemoveDir(ctx context.Context, path string) error {
	return f.client.FSRemove(mutationContext(ctx), path, true)
}

func (f *FS) Rename(ctx context.Context, oldPath, newPath string) error {
	return f.client.FSRename(mutationContext(ctx), oldPath, newPath)
}

func (f *FS) Truncate(ctx context.Context, path string, size int64) error {
	return f.client.FSTruncate(mutationContext(ctx), path, size)
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

// WaitIdle waits until the whole owner filesystem settles (all uploads and
// tasks), preserving the legacy whole-runtime idle contract for fs sync.
func (f *FS) WaitIdle(ctx context.Context, timeout time.Duration) error {
	return f.client.FSWaitIdle(ctx, timeout)
}

func entryFromDTO(entry control.FSEntry) drive.Entry {
	out := drive.Entry{
		ID:       entry.ID,
		ParentID: entry.ParentID,
		Name:     entry.Name,
		IsDir:    entry.IsDir,
		Size:     entry.Size,
		ModTime:  entry.ModTime,
	}
	if entry.RemoteName != "" {
		out = drive.WrapRemoteName(out, entry.RemoteName)
	}
	return out
}

var _ = drive.Entry{}

// DebugResolve resolves a namespace path to its backend identity so diag
// clients such as `fs list --remote-names` work against the Core data plane.
func (f *FS) DebugResolve(ctx context.Context, path string, includeRemoteName bool) (diagnostics.DebugResolveInfo, error) {
	info, err := f.client.FSResolve(ctx, path)
	if err != nil {
		return diagnostics.DebugResolveInfo{}, err
	}
	out := diagnostics.DebugResolveInfo{
		Path:      info.Path,
		PlainName: info.PlainName,
		IsDir:     info.IsDir,
	}
	if includeRemoteName {
		out.RemoteName = info.RemoteName
	}
	return out, nil
}
