package vfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

var ErrReadOnly = errors.New("vfs: read-only namespace path")
var ErrNotFound = errors.New("vfs: not found")

// FileSystem is the common API implemented by a single-drive VFS and a
// multi-drive Namespace.
type FileSystem interface {
	Start(ctx context.Context)
	Stat(ctx context.Context, path string) (drive.Entry, error)
	List(ctx context.Context, path string) ([]drive.Entry, error)
	Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error)
	Create(ctx context.Context, path string) error
	WriteAt(ctx context.Context, path string, data []byte, off int64) (int, error)
	Flush(ctx context.Context, path string) error
	Mkdir(ctx context.Context, path string) (drive.Entry, error)
	Remove(ctx context.Context, path string) error
	RemoveDir(ctx context.Context, path string) error
	Rename(ctx context.Context, oldPath, newPath string) error
	Truncate(ctx context.Context, path string, size int64) error
	Pending() []PendingFile
}

type RemoteLister interface {
	RemoteList(ctx context.Context, path string) ([]drive.Entry, error)
}

type DebugResolver interface {
	DebugResolve(ctx context.Context, path string, includeRemoteName bool) (DebugResolveInfo, error)
}

type DebugConsistencyChecker interface {
	DebugConsistency(ctx context.Context, path string) (ConsistencyReport, error)
}

type DebugStagingInspector interface {
	DebugStaging(ctx context.Context, path string) (DebugStagingReport, error)
}

// DriverHealth describes the live health-check result for one mount.
type DriverHealth struct {
	Mount     string    `json:"mount"`
	Driver    string    `json:"driver,omitempty"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	Latency   string    `json:"latency,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// DriverHealthChecker is implemented by VFS and Namespace to expose
// per-driver live health checks through the debug socket.
type DriverHealthChecker interface {
	DriverHealth(ctx context.Context, mountName string) ([]DriverHealth, error)
}

// NamedDriver pairs a mount name with its underlying drive.Driver.
// Used by the debug socket to expose driver-level operations.
type NamedDriver struct {
	Name   string
	Driver drive.Driver
}

// DriverProvider is implemented by VFS and Namespace to expose the
// underlying driver references for driver-level debugging.
type DriverProvider interface {
	Drivers() []NamedDriver
}

type Mount struct {
	Name string
	FS   *VFS
}

// Namespace mounts multiple VFS instances under one virtual root. The first
// path segment is the mount name: /quark/docs, /quark2/docs, /localfs/docs.
type Namespace struct {
	mu        sync.RWMutex
	mounts    map[string]*VFS
	createdAt time.Time
}

func NewNamespace(mounts []Mount) (*Namespace, error) {
	ns := &Namespace{mounts: map[string]*VFS{}, createdAt: time.Now()}
	for _, mount := range mounts {
		name := cleanMountName(mount.Name)
		if name == "" {
			return nil, fmt.Errorf("vfs: mount name required")
		}
		if mount.FS == nil {
			return nil, fmt.Errorf("vfs: mount %s has nil filesystem", name)
		}
		if _, exists := ns.mounts[name]; exists {
			return nil, fmt.Errorf("vfs: duplicate mount name %q", name)
		}
		ns.mounts[name] = mount.FS
	}
	return ns, nil
}

func (n *Namespace) Start(ctx context.Context) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, fs := range n.mounts {
		fs.Start(ctx)
	}
}

func (n *Namespace) Stat(ctx context.Context, path string) (drive.Entry, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return drive.Entry{}, err
	}
	if root {
		return drive.Entry{ID: "/", Name: "/", IsDir: true, ModTime: n.createdAt}, nil
	}
	if rest == "/" {
		name := strings.Trim(strings.TrimPrefix(cleanVirtual(path), "/"), "/")
		return drive.Entry{ID: "/" + name, Name: name, IsDir: true, ModTime: n.createdAt}, nil
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
		return fmt.Errorf("vfs: cannot rename across mounts")
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

func (n *Namespace) Pending() []PendingFile {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var pending []PendingFile
	for name, fs := range n.mounts {
		for _, item := range fs.Pending() {
			item.Path = joinVirtual("/"+name, strings.TrimPrefix(item.Path, "/"))
			pending = append(pending, item)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Path < pending[j].Path })
	return pending
}

func (n *Namespace) Space(ctx context.Context) (drive.Space, error) {
	n.mu.RLock()
	mounts := make([]*VFS, 0, len(n.mounts))
	for _, mount := range n.mounts {
		mounts = append(mounts, mount)
	}
	n.mu.RUnlock()

	var total drive.Space
	var firstErr error
	for _, mount := range mounts {
		space, err := mount.Space(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		total.Total += space.Total
		total.Free += space.Free
	}
	if total.Total == 0 && total.Free == 0 && firstErr != nil {
		return drive.Space{}, firstErr
	}
	return total, nil
}

func (n *Namespace) resolve(path string) (*VFS, string, bool, error) {
	path = cleanVirtual(path)
	if path == "/" {
		return nil, "/", true, nil
	}
	trimmed := strings.TrimPrefix(path, "/")
	name, rest, _ := strings.Cut(trimmed, "/")
	name = cleanMountName(name)
	n.mu.RLock()
	mount := n.mounts[name]
	n.mu.RUnlock()
	if mount == nil {
		return nil, "", false, fmt.Errorf("%w: unknown mount %q", ErrNotFound, name)
	}
	if rest == "" {
		return mount, "/", false, nil
	}
	return mount, "/" + rest, false, nil
}

func (n *Namespace) rootEntries() []drive.Entry {
	n.mu.RLock()
	defer n.mu.RUnlock()
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]drive.Entry, 0, len(names))
	for _, name := range names {
		entries = append(entries, drive.Entry{
			ID:      "/" + name,
			Name:    name,
			IsDir:   true,
			ModTime: n.createdAt,
		})
	}
	return entries
}

func cleanMountName(name string) string {
	return strings.Trim(strings.TrimSpace(name), "/")
}

var _ FileSystem = (*VFS)(nil)
var _ FileSystem = (*Namespace)(nil)
