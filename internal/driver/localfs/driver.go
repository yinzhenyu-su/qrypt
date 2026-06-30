package localfs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/osutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type Driver struct {
	root string
}

func init() {
	drive.Register("localfs", func(params drive.Params) (drive.Driver, error) {
		root := params["root"]
		if root == "" {
			root = params["local_root"]
		}
		if root == "" {
			return nil, fmt.Errorf("localfs: missing root")
		}
		return New(root), nil
	},
		drive.ParamDef{
			Name:        "root",
			Type:        "string",
			Required:    true,
			Description: "Local filesystem directory path",
			Example:     "/tmp/qrypt-remote",
		},
		drive.ParamDef{
			Name:        "local_root",
			Type:        "string",
			Description: "Alias for root (fallback if root is empty)",
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
			"root": d.root,
		},
	}, nil
}

func (d *Driver) HealthCheck(ctx context.Context) drive.HealthStatus {
	start := time.Now()
	status := drive.HealthStatus{Driver: "localfs", CheckedAt: start}
	if _, err := os.Stat(d.root); err != nil {
		status.Error = err.Error()
		status.Latency = time.Since(start).String()
		return status
	}
	status.OK = true
	status.Latency = time.Since(start).String()
	return status
}

func (d *Driver) ResolveRemoteName(ctx context.Context, plainName string) (drive.RemoteNameInfo, error) {
	return drive.RemoteNameInfo{PlainName: plainName, RemoteName: plainName}, nil
}

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	dir := d.resolve(parentID)
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("localfs: readdir %s: %w", dir, err)
	}
	entries := make([]drive.Entry, 0, len(items))
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			continue
		}
		entries = append(entries, drive.Entry{
			ID:       filepath.Join(dir, item.Name()),
			ParentID: dir,
			Name:     item.Name(),
			IsDir:    item.IsDir(),
			Size:     info.Size(),
			ModTime:  info.ModTime(),
		})
	}
	return entries, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	rc, err := osutil.OpenRead(entry.ID, offset, size)
	if err != nil {
		return nil, fmt.Errorf("localfs: open %s: %w", entry.ID, err)
	}
	return rc, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	path := filepath.Join(d.resolve(parentID), name)
	if err := os.Mkdir(path, 0o755); err != nil {
		return drive.Entry{}, err
	}
	return drive.Entry{ID: path, ParentID: d.resolve(parentID), Name: name, IsDir: true, ModTime: time.Now()}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	return os.Rename(entry.ID, filepath.Join(d.resolve(dstParentID), filepath.Base(entry.ID)))
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	return os.Rename(entry.ID, filepath.Join(filepath.Dir(entry.ID), newName))
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	if entry.IsDir {
		return os.RemoveAll(entry.ID)
	}
	return os.Remove(entry.ID)
}

func (d *Driver) Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (drive.Entry, error) {
	parent := d.resolve(parentID)
	path := filepath.Join(parent, name)
	f, err := os.Create(path)
	if err != nil {
		return drive.Entry{}, err
	}
	defer f.Close()
	if _, err := io.Copy(f, body); err != nil {
		return drive.Entry{}, err
	}
	info, err := f.Stat()
	if err != nil {
		return drive.Entry{ID: path, ParentID: parent, Name: name}, nil
	}
	return drive.Entry{ID: path, ParentID: parent, Name: name, Size: info.Size(), ModTime: info.ModTime()}, nil
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

var _ drive.Driver = (*Driver)(nil)
var _ drive.Writer = (*Driver)(nil)
var _ drive.Uploader = (*Driver)(nil)
var _ drive.SpaceQuerier = (*Driver)(nil)
var _ drive.PathResolver = (*Driver)(nil)
var _ drive.Debugger = (*Driver)(nil)
var _ drive.RemoteNameResolver = (*Driver)(nil)
var _ drive.HealthChecker = (*Driver)(nil)
