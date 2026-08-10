package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const uploadCopyChunkSize = 256 * 1024

// UploadService owns business-level upload semantics. It deliberately writes
// through the internal filesystem API instead of treating FUSE as the upload
// entry point.
type UploadService struct {
	resolver UploadDestinationResolver
	backend  UploadBackend
}

type UploadBackend interface {
	Stat(ctx context.Context, path string) (drive.Entry, error)
	Create(ctx context.Context, path string) error
	WriteAt(ctx context.Context, path string, data []byte, off int64) (int, error)
	Flush(ctx context.Context, path string) error
	Mkdir(ctx context.Context, path string) (drive.Entry, error)
	Remove(ctx context.Context, path string) error
	RefreshPath(path string)
	MountForPath(path string) string
}

// vfsBackendFS is the surface VFSUploadBackend needs: full file operations
// plus cache refresh so the upload path invalidates listings it touched.
type vfsBackendFS interface {
	vfs.FileSystem
	vfs.PathRefresher
}

type VFSUploadBackend struct {
	fs vfsBackendFS
}

func NewVFSUploadBackend(fs vfsBackendFS) VFSUploadBackend {
	return VFSUploadBackend{fs: fs}
}

func (b VFSUploadBackend) Stat(ctx context.Context, path string) (drive.Entry, error) {
	return b.fs.Stat(ctx, path)
}

func (b VFSUploadBackend) Create(ctx context.Context, path string) error {
	return b.fs.Create(ctx, path)
}

func (b VFSUploadBackend) WriteAt(ctx context.Context, path string, data []byte, off int64) (int, error) {
	return b.fs.WriteAt(ctx, path, data, off)
}

func (b VFSUploadBackend) Flush(ctx context.Context, path string) error {
	return b.fs.Flush(ctx, path)
}

func (b VFSUploadBackend) Mkdir(ctx context.Context, path string) (drive.Entry, error) {
	return b.fs.Mkdir(ctx, path)
}

func (b VFSUploadBackend) Remove(ctx context.Context, path string) error {
	return b.fs.Remove(ctx, path)
}

func (b VFSUploadBackend) RefreshPath(path string) {
	b.fs.RefreshPath(path)
}

func (b VFSUploadBackend) MountForPath(path string) string {
	mount, _, _ := moveMounts(path, path, b.fs)
	return mount
}

type UploadLocalFileRequest struct {
	LocalPath      string
	DestPath       string
	ConflictPolicy string
}

type UploadResult struct {
	Entry   drive.Entry
	Path    string
	Mount   string
	Skipped bool
	Instant bool
}

func (c *Core) UploadService() (*UploadService, error) {
	if c == nil || c.fs == nil {
		return nil, fmt.Errorf("core: closed")
	}
	return NewUploadService(NewUploadDestinationResolver(c.defaultUploadMount, c.defaultUploadPath), NewVFSUploadBackend(c.fs)), nil
}

func NewUploadService(resolver UploadDestinationResolver, backend UploadBackend) *UploadService {
	return &UploadService{resolver: resolver, backend: backend}
}

func (s *UploadService) UploadLocalFile(ctx context.Context, req UploadLocalFileRequest) (drive.Entry, error) {
	result, err := s.UploadLocalFileResult(ctx, req)
	return result.Entry, err
}

func (s *UploadService) UploadLocalFileResult(ctx context.Context, req UploadLocalFileRequest) (UploadResult, error) {
	if s == nil || s.backend == nil {
		return UploadResult{}, fmt.Errorf("core: closed")
	}
	if strings.TrimSpace(req.LocalPath) == "" {
		return UploadResult{}, fmt.Errorf("core: local path required")
	}
	resolvedRemotePath, err := s.ResolveDestination(ctx, req.DestPath, filepath.Base(req.LocalPath))
	if err != nil {
		return UploadResult{}, err
	}
	if existing, skipped, err := s.applyConflictPolicy(ctx, resolvedRemotePath, req.ConflictPolicy); err != nil {
		return UploadResult{}, err
	} else if skipped {
		return s.resultForEntry(resolvedRemotePath, existing, true), nil
	}
	f, err := os.Open(req.LocalPath)
	if err != nil {
		return UploadResult{}, err
	}
	defer f.Close()
	if err := s.BeginStream(ctx, resolvedRemotePath); err != nil {
		return UploadResult{}, err
	}
	buf := make([]byte, uploadCopyChunkSize)
	var off int64
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			written, err := s.WriteStream(ctx, resolvedRemotePath, buf[:n], off)
			if err != nil {
				return UploadResult{}, err
			}
			if written != n {
				return UploadResult{}, fmt.Errorf("core: short staging write: wrote %d of %d", written, n)
			}
			off += int64(written)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return UploadResult{}, readErr
		}
	}
	entry, err := s.FinishStream(ctx, resolvedRemotePath)
	if err != nil {
		return UploadResult{}, err
	}
	return s.resultForEntry(resolvedRemotePath, entry, false), nil
}

func (s *UploadService) ResolveDestination(ctx context.Context, destPath, fallbackName string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("core: closed")
	}
	resolved, err := s.resolver.resolve(destPath, fallbackName)
	if err != nil {
		return "", err
	}
	if resolved.DefaultDir != "" {
		if err := s.ensureDefaultUploadDir(ctx, resolved.DefaultDir); err != nil {
			return "", err
		}
	}
	return resolved.Path, nil
}

func (s *UploadService) PreviewDestination(destPath, fallbackName string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("core: closed")
	}
	resolved, err := s.resolver.resolve(destPath, fallbackName)
	if err != nil {
		return "", err
	}
	return resolved.Path, nil
}

func (s *UploadService) BeginStream(ctx context.Context, remotePath string) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("core: closed")
	}
	if strings.TrimSpace(remotePath) == "" {
		return fmt.Errorf("core: remote path required")
	}
	return s.backend.Create(ctx, remotePath)
}

func (s *UploadService) WriteStream(ctx context.Context, remotePath string, data []byte, offset int64) (int, error) {
	if s == nil || s.backend == nil {
		return 0, fmt.Errorf("core: closed")
	}
	if offset < 0 {
		return 0, fmt.Errorf("core: offset must be non-negative")
	}
	return s.backend.WriteAt(ctx, remotePath, data, offset)
}

func (s *UploadService) FinishStream(ctx context.Context, remotePath string) (drive.Entry, error) {
	if s == nil || s.backend == nil {
		return drive.Entry{}, fmt.Errorf("core: closed")
	}
	if err := s.backend.Flush(ctx, remotePath); err != nil {
		return drive.Entry{}, err
	}
	entry, err := s.backend.Stat(ctx, remotePath)
	if err != nil {
		return drive.Entry{}, err
	}
	s.backend.RefreshPath(path.Dir(remotePath))
	return entry, nil
}

func (s *UploadService) CancelStream(ctx context.Context, remotePath string) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("core: closed")
	}
	return s.backend.Remove(ctx, remotePath)
}

func (s *UploadService) applyConflictPolicy(ctx context.Context, remotePath, policy string) (drive.Entry, bool, error) {
	policy = normalizeUploadConflictPolicy(policy)
	switch policy {
	case "", "overwrite", "replace":
		return drive.Entry{}, false, nil
	case "fail", "error":
		_, err := s.backend.Stat(ctx, remotePath)
		if err == nil {
			return drive.Entry{}, false, fmt.Errorf("core: upload destination %q already exists", remotePath)
		}
		if errors.Is(err, vfs.ErrNotFound) {
			return drive.Entry{}, false, nil
		}
		return drive.Entry{}, false, err
	case "skip":
		entry, err := s.backend.Stat(ctx, remotePath)
		if err == nil {
			return entry, true, nil
		}
		if errors.Is(err, vfs.ErrNotFound) {
			return drive.Entry{}, false, nil
		}
		return drive.Entry{}, false, err
	default:
		return drive.Entry{}, false, fmt.Errorf("core: unsupported upload conflict_policy %q", policy)
	}
}

func normalizeUploadConflictPolicy(policy string) string {
	return strings.ToLower(strings.TrimSpace(policy))
}

func (s *UploadService) resultForEntry(remotePath string, entry drive.Entry, skipped bool) UploadResult {
	return UploadResult{
		Entry:   entry,
		Path:    remotePath,
		Mount:   s.backend.MountForPath(remotePath),
		Skipped: skipped,
	}
}

func (r *UploadResult) applyRemoteTask(remoteTask task.Task) {
	if r == nil || remoteTask.Detail == nil {
		return
	}
	if id, ok := remoteTask.Detail["result_remote_id"].(string); ok && id != "" {
		r.Entry.ID = id
	}
	if instant, ok := remoteTask.Detail["instant"].(bool); ok {
		r.Instant = instant
	}
}

func (s *UploadService) ensureDefaultUploadDir(ctx context.Context, dir string) error {
	if s == nil || s.backend == nil || dir == "" {
		return nil
	}
	_, err := s.backend.Mkdir(ctx, dir)
	if err != nil {
		return fmt.Errorf("core: create upload.default_path %q: %w", dir, err)
	}
	s.backend.RefreshPath(path.Dir(dir))
	return nil
}
