package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	drivers "github.com/yinzhenyu/qrypt/pkg/drivers/all"
)

// ScopedFSBackend is implemented by the embedding app to expose a
// user-authorized directory as a scopedfs driver backend.
//
// Android implementations typically map rootToken to a persisted SAF tree
// URI. iOS implementations typically map rootToken to a stored
// security-scoped bookmark.
type ScopedFSBackend interface {
	Stat(rootToken, id string) (string, error)
	List(rootToken, parentID string) (string, error)
	OpenRead(rootToken, id string, offset int64) (int64, error)
	Read(handle int64, dst []byte) (int, error)
	CloseRead(handle int64) error
	Mkdir(rootToken, parentID, name string) (string, error)
	Move(rootToken, id, name string, isDir bool, dstParentID string) error
	Rename(rootToken, id string, isDir bool, newName string) error
	Remove(rootToken, id string, isDir bool) error
	CreateWrite(rootToken, parentID, name string) (int64, error)
	Write(handle int64, data []byte) (int, error)
	CloseWrite(handle int64) (string, error)
	AbortWrite(handle int64) error
}

type scopedEntryJSON struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id,omitempty"`
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size,omitempty"`
	ModTime   string `json:"mod_time,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// SetScopedFSBackendJSON installs the platform-authorized directory backend
// used by scopedfs mounts whose params.backend is "mobile" or omitted.
func SetScopedFSBackendJSON(backend ScopedFSBackend) string {
	if backend == nil {
		return resultJSON(nil, wrapError(fmt.Errorf("mobile: scopedfs backend must not be nil")))
	}
	err := drivers.RegisterScopedFSBackend("mobile", mobileScopedFSBackend{backend: backend})
	return resultJSON(err == nil, wrapError(err))
}

func ClearScopedFSBackendJSON() string {
	drivers.UnregisterScopedFSBackend("mobile")
	return resultJSON(true, nil)
}

type mobileScopedFSBackend struct {
	backend ScopedFSBackend
}

func (b mobileScopedFSBackend) Stat(ctx context.Context, rootToken, id string) (drive.Entry, error) {
	raw, err := b.backend.Stat(rootToken, id)
	if err != nil {
		return drive.Entry{}, wrapError(err)
	}
	return parseScopedEntry(raw)
}

func (b mobileScopedFSBackend) List(ctx context.Context, rootToken, parentID string) ([]drive.Entry, error) {
	raw, err := b.backend.List(rootToken, parentID)
	if err != nil {
		return nil, wrapError(err)
	}
	var in []scopedEntryJSON
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil, fmt.Errorf("mobile: invalid scopedfs list json: %w", err)
	}
	entries := make([]drive.Entry, 0, len(in))
	for _, item := range in {
		entry, err := scopedEntryFromJSON(item)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (b mobileScopedFSBackend) OpenRead(ctx context.Context, rootToken, id string, offset int64) (io.ReadCloser, error) {
	handle, err := b.backend.OpenRead(rootToken, id, offset)
	if err != nil {
		return nil, wrapError(err)
	}
	return &scopedReadHandle{backend: b.backend, handle: handle}, nil
}

func (b mobileScopedFSBackend) Mkdir(ctx context.Context, rootToken, parentID, name string) (drive.Entry, error) {
	raw, err := b.backend.Mkdir(rootToken, parentID, name)
	if err != nil {
		return drive.Entry{}, wrapError(err)
	}
	return parseScopedEntry(raw)
}

func (b mobileScopedFSBackend) Move(ctx context.Context, rootToken string, entry drive.Entry, dstParentID string) error {
	return wrapError(b.backend.Move(rootToken, entry.ID, entry.Name, entry.IsDir, dstParentID))
}

func (b mobileScopedFSBackend) Rename(ctx context.Context, rootToken string, entry drive.Entry, newName string) error {
	return wrapError(b.backend.Rename(rootToken, entry.ID, entry.IsDir, newName))
}

func (b mobileScopedFSBackend) Remove(ctx context.Context, rootToken string, entry drive.Entry) error {
	return wrapError(b.backend.Remove(rootToken, entry.ID, entry.IsDir))
}

func (b mobileScopedFSBackend) CreateWrite(ctx context.Context, rootToken, parentID, name string) (drivers.ScopedFSWriteHandle, error) {
	handle, err := b.backend.CreateWrite(rootToken, parentID, name)
	if err != nil {
		return nil, wrapError(err)
	}
	return &scopedWriteHandle{backend: b.backend, handle: handle}, nil
}

type scopedReadHandle struct {
	backend ScopedFSBackend
	handle  int64
	closed  bool
}

func (h *scopedReadHandle) Read(p []byte) (int, error) {
	if h.closed {
		return 0, io.EOF
	}
	n, err := h.backend.Read(h.handle, p)
	if err != nil {
		return n, wrapError(err)
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (h *scopedReadHandle) Close() error {
	if h.closed {
		return nil
	}
	h.closed = true
	return wrapError(h.backend.CloseRead(h.handle))
}

type scopedWriteHandle struct {
	backend ScopedFSBackend
	handle  int64
	closed  bool
}

func (h *scopedWriteHandle) Write(p []byte) (int, error) {
	if h.closed {
		return 0, io.ErrClosedPipe
	}
	return h.backend.Write(h.handle, p)
}

func (h *scopedWriteHandle) Close() (drive.Entry, error) {
	if h.closed {
		return drive.Entry{}, io.ErrClosedPipe
	}
	h.closed = true
	raw, err := h.backend.CloseWrite(h.handle)
	if err != nil {
		return drive.Entry{}, wrapError(err)
	}
	return parseScopedEntry(raw)
}

func (h *scopedWriteHandle) Abort() error {
	if h.closed {
		return nil
	}
	h.closed = true
	return wrapError(h.backend.AbortWrite(h.handle))
}

func parseScopedEntry(raw string) (drive.Entry, error) {
	var in scopedEntryJSON
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return drive.Entry{}, fmt.Errorf("mobile: invalid scopedfs entry json: %w", err)
	}
	return scopedEntryFromJSON(in)
}

func scopedEntryFromJSON(in scopedEntryJSON) (drive.Entry, error) {
	if in.ID == "" {
		return drive.Entry{}, fmt.Errorf("mobile: scopedfs entry id required")
	}
	if in.Name == "" {
		return drive.Entry{}, fmt.Errorf("mobile: scopedfs entry name required")
	}
	modTime, err := parseScopedTime(in.ModTime)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("mobile: invalid scopedfs mod_time: %w", err)
	}
	createdAt, err := parseScopedTime(in.CreatedAt)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("mobile: invalid scopedfs created_at: %w", err)
	}
	updatedAt, err := parseScopedTime(in.UpdatedAt)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("mobile: invalid scopedfs updated_at: %w", err)
	}
	return drive.Entry{
		ID:        in.ID,
		ParentID:  in.ParentID,
		Name:      in.Name,
		IsDir:     in.IsDir,
		Size:      in.Size,
		ModTime:   modTime,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

func parseScopedTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, raw)
}

var _ drivers.ScopedFSBackend = mobileScopedFSBackend{}
