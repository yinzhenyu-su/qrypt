package localfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

type Driver struct {
	drive.UnsupportedOperations
	root string
}

func init() {
	drive.Register("localfs", func(params drive.Params) (drive.Driver, error) {
		root := params["root_path"]
		if root == "" {
			return nil, fmt.Errorf("localfs: missing root_path")
		}
		return New(root), nil
	},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Required:    true,
			Description: "Local filesystem root directory path",
			Example:     "/tmp/qrypt-remote",
		},
	)
}

func New(root string) *Driver {
	return &Driver{root: filepath.Clean(root)}
}

func (d *Driver) Init(ctx context.Context) error {
	info, err := os.Stat(d.root)
	if err != nil {
		return fmt.Errorf("localfs: stat root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("localfs: root is not a directory: %s", d.root)
	}
	return nil
}

func (d *Driver) Drop(ctx context.Context) error { return nil }

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{
		Driver:      "localfs",
		Health:      "ok",
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			drive.DebugStatRootPath: d.root,
		},
	}, nil
}

func (d *Driver) metricEvents(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

func (d *Driver) ResolveRemoteName(ctx context.Context, plainName string) (drive.RemoteNameInfo, error) {
	return drive.RemoteNameInfo{PlainName: plainName, RemoteName: plainName}, nil
}

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := d.resolve(parentID)
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, classifyLocalError(fmt.Errorf("localfs: readdir %s: %w", dir, err))
	}
	entries := make([]drive.Entry, 0, len(items))
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			continue
		}
		modTime := info.ModTime()
		entries = append(entries, drive.Entry{
			ID:        filepath.Join(dir, item.Name()),
			ParentID:  dir,
			Name:      item.Name(),
			IsDir:     item.IsDir(),
			Size:      info.Size(),
			ModTime:   modTime,
			UpdatedAt: modTime,
		})
	}
	return entries, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rc, err := util.OpenRead(entry.ID, offset, size)
	if err != nil {
		return nil, classifyLocalError(fmt.Errorf("localfs: open %s: %w", entry.ID, err))
	}
	return rc, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	if err := ctx.Err(); err != nil {
		return drive.Entry{}, err
	}
	path := filepath.Join(d.resolve(parentID), name)
	if err := os.Mkdir(path, 0o755); err != nil {
		return drive.Entry{}, classifyLocalError(err)
	}
	now := time.Now()
	return drive.Entry{ID: path, ParentID: d.resolve(parentID), Name: name, IsDir: true, ModTime: now, CreatedAt: now, UpdatedAt: now}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	return os.Rename(entry.ID, filepath.Join(d.resolve(dstParentID), filepath.Base(entry.ID)))
}

// Copy implements drive.ServerSideCopier: duplicates a stored file with an
// OS-level copy (no data round trip through qrypt) and preserves the source
// mtime, keeping the CapabilityMtime + CapabilityServerSideCopy contract
// (sync converges on mtime instead of re-copying every run). Directory
// copies are rejected per the contract.
func (d *Driver) Copy(ctx context.Context, src drive.Entry, dstParentID, dstName string) (drive.Entry, error) {
	if err := ctx.Err(); err != nil {
		return drive.Entry{}, err
	}
	if src.IsDir {
		return drive.Entry{}, drive.ErrUnsupported
	}
	dstPath := filepath.Join(d.resolve(dstParentID), dstName)
	if err := copyFilePreservingMtime(src.ID, dstPath); err != nil {
		return drive.Entry{}, classifyLocalError(err)
	}
	info, err := os.Stat(dstPath)
	if err != nil {
		return drive.Entry{}, classifyLocalError(err)
	}
	modTime := info.ModTime()
	return drive.Entry{ID: dstPath, ParentID: d.resolve(dstParentID), Name: dstName, Size: info.Size(), ModTime: modTime, UpdatedAt: modTime}, nil
}

// copyFilePreservingMtime copies a regular file and stamps the source mtime
// onto the copy so a server-side copy is indistinguishable from the source
// in size and timestamp.
func copyFilePreservingMtime(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return os.Chtimes(dstPath, info.ModTime(), info.ModTime())
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	return os.Rename(entry.ID, filepath.Join(filepath.Dir(entry.ID), newName))
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry.IsDir {
		return os.RemoveAll(entry.ID)
	}
	return classifyLocalError(os.Remove(entry.ID))
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	if err := ctx.Err(); err != nil {
		return drive.Entry{}, err
	}
	parentID, name, source := req.ParentID, req.Name, req.Source
	body, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("localfs: open source: %w", err)
	}
	defer body.Close()

	parent := d.resolve(parentID)
	path := filepath.Join(parent, name)
	f, err := os.Create(path)
	if err != nil {
		return drive.Entry{}, err
	}
	defer f.Close()
	if _, err := io.Copy(f, drive.NewUploadProgressReader(req.Progress, body)); err != nil {
		return drive.Entry{}, err
	}
	if err := f.Sync(); err != nil {
		return drive.Entry{}, err
	}
	if err := f.Close(); err != nil {
		return drive.Entry{}, err
	}
	if !req.ModTime.IsZero() {
		if err := os.Chtimes(path, req.ModTime, req.ModTime); err != nil {
			return drive.Entry{}, fmt.Errorf("localfs: set mtime: %w", err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return drive.Entry{ID: path, ParentID: parent, Name: name, Size: source.Size(), ModTime: req.ModTime}, nil
	}
	modTime := info.ModTime()
	return drive.Entry{ID: path, ParentID: parent, Name: name, Size: info.Size(), ModTime: modTime, UpdatedAt: modTime}, nil
}

func (d *Driver) ResolvePath(ctx context.Context, path string) (string, error) {
	if path == "" || path == "/" || path == "." {
		return d.root, nil
	}
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) {
		rel, err := filepath.Rel(d.root, clean)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return clean, nil
		}
		return "", fmt.Errorf("localfs: path escapes root: %s", path)
	}
	target := filepath.Join(d.root, clean)
	rel, err := filepath.Rel(d.root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("localfs: path escapes root: %s", path)
	}
	return target, nil
}

func (d *Driver) resolve(id string) string {
	if id == "" || id == "0" || id == "/" {
		return d.root
	}
	return id
}

// classifyLocalError maps OS not-found errors to drive.ErrNotFound so the
// whole error layer can classify them with errors.Is.
func classifyLocalError(err error) error {
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %v", drive.ErrNotFound, err)
	}
	return err
}

var _ drive.Driver = (*Driver)(nil)
