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
	"github.com/yinzhenyu/qrypt/pkg/task"
)

var ErrReadOnly = errors.New("vfs: read-only namespace path")
var ErrNotFound = drive.ErrNotFound
var ErrCrossMount = errors.New("vfs: cross-mount rename")

// Reader is the read-only subset of FileSystem.
type Reader interface {
	Stat(ctx context.Context, path string) (drive.Entry, error)
	List(ctx context.Context, path string) ([]drive.Entry, error)
	Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error)
}

// Writer is the write subset of FileSystem.
type Writer interface {
	Create(ctx context.Context, path string) error
	WriteAt(ctx context.Context, path string, data []byte, off int64) (int, error)
	Flush(ctx context.Context, path string) error
	Mkdir(ctx context.Context, path string) (drive.Entry, error)
	Remove(ctx context.Context, path string) error
	RemoveDir(ctx context.Context, path string) error
	Rename(ctx context.Context, oldPath, newPath string) error
	Truncate(ctx context.Context, path string, size int64) error
}

// Lifecycle starts a filesystem's background workers. It is intentionally
// separate from FileSystem: constructing a filesystem (New/NewNamespace)
// does not run anything, and read-only consumers never need to start one.
type Lifecycle interface {
	Start(ctx context.Context)
}

// PathRefresher invalidates the directory listing cache for path so the
// next List call fetches fresh data from the remote driver. Cache/view
// control is an optional capability, not a file operation.
type PathRefresher interface {
	RefreshPath(path string)
}

// FileSystem is the common file-operation API implemented by a single-drive
// VFS and a multi-drive Namespace. Runtime lifecycle (Start) and cache/view
// control (RefreshPath) live in separate optional interfaces so consumers
// depend only on the file operations they actually use.
type FileSystem interface {
	Reader
	Writer
}

type UploadInspector interface {
	PendingUploads() []PendingUpload
}

type RemoteLister interface {
	RemoteList(ctx context.Context, path string) ([]drive.Entry, error)
}

// Optional capability interfaces are grouped by consumer role:
//
//	file operations  FileSystem (Reader + Writer), Lifecycle, PathRefresher
//	runtime caps     UploadInspector, RemoteLister, HashProvider,
//	                 EncryptedHashProvider, ModTimeWriter, SpaceProvider,
//	                 MountSpaceProvider
//	diagnostics      DebugResolver, DebugConsistencyChecker,
//	                 DebugStagingInspector, DebugMountSnapshotter,
//	                 DebugSnapshotProvider, RemoteIDResolver,
//	                 MountHealthChecker, DriverProvider
//	test control    DebugUploadCancelInjector (in debug_fault.go)
//
// Naming convention: *Resolver resolves ids/paths, *Inspector exposes
// internals read-only, *Provider exposes a value, *Checker reports health,
// *Snapshotter/ReadCache* control snapshot and cache surfaces. Interfaces
// stay narrow and are asserted on demand; they are not merged into a single
// debug interface so fakes and implementations stay small.

// DebugResolver resolves a virtual path to its remote identity.
type DebugResolver interface {
	DebugResolve(ctx context.Context, path string, includeRemoteName bool) (DebugResolveInfo, error)
}

type DebugConsistencyChecker interface {
	DebugConsistency(ctx context.Context, path string) (ConsistencyReport, error)
}

type DebugStagingInspector interface {
	DebugStaging(ctx context.Context, path string) (DebugStagingReport, error)
}

type DebugMountSnapshotter interface {
	DebugSnapshotForMounts(mountNames []string) DebugSnapshot
}

// MountHealth describes recent runtime health for one mount.
type MountHealth struct {
	Mount     string                   `json:"mount"`
	OK        bool                     `json:"ok"`
	Level     string                   `json:"level,omitempty"`
	Error     string                   `json:"error,omitempty"`
	CheckedAt time.Time                `json:"checked_at"`
	Success   int                      `json:"success"`
	Errors    int                      `json:"errors"`
	Ops       map[string]MountHealthOp `json:"ops,omitempty"`
}

type MountHealthOp struct {
	Success     int       `json:"success"`
	Errors      int       `json:"errors"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitempty"`
}

// MountHealthChecker is implemented by VFS and Namespace to expose
// per-mount runtime health through the debug socket.
type MountHealthChecker interface {
	MountHealth(ctx context.Context, mountName string) ([]MountHealth, error)
}

// NamedDriver pairs a mount name with its underlying drive.Driver.
// Used by the debug socket to expose driver-level operations.
type NamedDriver struct {
	Name        string
	Driver      drive.Driver
	TestEnabled bool
}

// DriverProvider is implemented by VFS and Namespace to expose the
// underlying driver references for driver-level debugging.
type DriverProvider interface {
	Drivers() []NamedDriver
}

// The interfaces below name optional capabilities a FileSystem may expose.
// Consumers assert against the named interface instead of an inline anonymous
// one, so fakes are easy to construct and the capability surface is
// discoverable in one place.

// HashProvider reports the backend hash of a stored object.
type HashProvider interface {
	RemoteHash(ctx context.Context, path string) (drive.HashAlgorithm, string, error)
}

// EncryptedHashProvider computes the hash the plaintext would have when
// encrypted with the mount's cipher (nonce from the remote header).
type EncryptedHashProvider interface {
	EncryptedHash(ctx context.Context, path string, plain io.Reader, plainSize int64, algorithm drive.HashAlgorithm) (string, error)
}

// ModTimeWriter stamps a backend object's mtime.
type ModTimeWriter interface {
	SetModTime(ctx context.Context, path string, modTime time.Time) error
}

// SpaceProvider reports aggregate backend capacity.
type SpaceProvider interface {
	Space(ctx context.Context) (drive.Space, error)
}

// MountSpaceProvider reports per-mount capacity breakdown.
type MountSpaceProvider interface {
	MountSpaces(ctx context.Context) []MountSpace
}

// DebugSnapshotProvider exposes the filesystem debug snapshot.
type DebugSnapshotProvider interface {
	DebugSnapshot() DebugSnapshot
}

// ReadCacheCleaner drops the read-cache files.
type ReadCacheCleaner interface {
	ClearReadCache() error
}

// ReadCacheFlusher flushes pending read-cache writes.
type ReadCacheFlusher interface {
	FlushReadCache() error
}

// RemoteIDResolver resolves a backend object by its remote id.
type RemoteIDResolver interface {
	DebugResolveByRemoteID(ctx context.Context, remoteID string) (*DebugResolveInfo, string, error)
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

func (n *Namespace) FlushReadCache() error {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var lastErr error
	for _, fs := range n.mounts {
		if err := fs.FlushReadCache(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (n *Namespace) ClearReadCache() error {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var lastErr error
	for _, fs := range n.mounts {
		if err := fs.ClearReadCache(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (n *Namespace) CloseReadCache() error {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var lastErr error
	for _, fs := range n.mounts {
		if err := fs.CloseReadCache(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (n *Namespace) StartDirectoryPrefetch(ctx context.Context) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, fs := range n.mounts {
		fs.StartDirectoryPrefetch(ctx)
	}
}

func (n *Namespace) Stat(ctx context.Context, path string) (drive.Entry, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return drive.Entry{}, err
	}
	if root {
		return drive.Entry{ID: "/", Name: "/", IsDir: true, ModTime: n.createdAt, CreatedAt: n.createdAt, UpdatedAt: n.createdAt}, nil
	}
	if rest == "/" {
		name := strings.Trim(strings.TrimPrefix(cleanVirtual(path), "/"), "/")
		return drive.Entry{ID: "/" + name, ParentID: "/", Name: name, IsDir: true, ModTime: n.createdAt, CreatedAt: n.createdAt, UpdatedAt: n.createdAt}, nil
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

func (n *Namespace) ListPage(ctx context.Context, path, cursor string, limit int) (ListPageResult, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return ListPageResult{}, err
	}
	if root {
		return paginateEntries(n.rootEntries(), cursor, limit), nil
	}
	return mount.ListPage(ctx, rest, cursor, limit)
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
		return fmt.Errorf("%w: %s -> %s", ErrCrossMount, oldPath, newPath)
	}
	return oldMount.Rename(ctx, oldRest, newRest)
}

func (n *Namespace) RefreshPath(path string) {
	mount, rest, _, err := n.resolve(path)
	if err != nil || mount == nil {
		return
	}
	mount.RefreshPath(rest)
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

func (n *Namespace) PendingUploads() []PendingUpload {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var pending []PendingUpload
	for name, fs := range n.mounts {
		for _, item := range fs.PendingUploads() {
			item.Path = joinVirtual("/"+name, strings.TrimPrefix(item.Path, "/"))
			pending = append(pending, item)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Path < pending[j].Path })
	return pending
}

func (n *Namespace) Tasks(filter task.Filter) []task.Task {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var tasks []task.Task
	for name, fs := range n.mounts {
		mountFilter, ok := namespaceMountTaskFilter(filter, name)
		if !ok {
			continue
		}
		for _, item := range fs.Tasks(mountFilter) {
			item = namespaceTask(name, item)
			if filter.Match(item) {
				tasks = append(tasks, item)
			}
		}
	}
	sortTasks(tasks)
	if filter.Limit > 0 && len(tasks) > filter.Limit {
		tasks = tasks[:filter.Limit]
	}
	return tasks
}

func (n *Namespace) ListTasks(ctx context.Context, filter task.Filter) ([]task.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return n.Tasks(filter), nil
}

// dismisser is satisfied by VFS instances that support task dismissal.
type dismisser interface {
	DismissTask(ctx context.Context, id string) error
	DismissFinishedTasks(ctx context.Context, filter task.Filter) (int, error)
}

func (n *Namespace) DismissTask(ctx context.Context, id string) error {
	mount, rest, ok := splitNamespaceTaskID(id)
	if !ok {
		return task.ErrNotFound
	}
	n.mu.RLock()
	fs, ok := n.mounts[mount]
	n.mu.RUnlock()
	if !ok {
		return task.ErrNotFound
	}
	dismissible, ok := interface{}(fs).(dismisser)
	if !ok {
		return task.ErrNotFound
	}
	return dismissible.DismissTask(ctx, rest)
}

func (n *Namespace) DismissFinishedTasks(ctx context.Context, filter task.Filter) (int, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	removed := 0
	for name, fs := range n.mounts {
		mountFilter, ok := namespaceMountTaskFilter(filter, name)
		if !ok {
			continue
		}
		dismissible, ok := interface{}(fs).(dismisser)
		if !ok {
			continue
		}
		n, err := dismissible.DismissFinishedTasks(ctx, mountFilter)
		if err != nil {
			return removed, err
		}
		removed += n
	}
	return removed, nil
}

func (n *Namespace) GetTask(ctx context.Context, id string) (task.Task, error) {
	if err := ctx.Err(); err != nil {
		return task.Task{}, err
	}
	for _, item := range n.Tasks(task.Filter{}) {
		if item.ID == id {
			return item, nil
		}
	}
	return task.Task{}, task.ErrNotFound
}

func namespaceMountTaskFilter(filter task.Filter, mount string) (task.Filter, bool) {
	if filter.Mount != "" && filter.Mount != mount {
		return task.Filter{}, false
	}
	mountFilter := filter
	if filter.Path != "" {
		mountName, rest, root := splitNamespacePath(filter.Path)
		if root || mountName != mount {
			return task.Filter{}, false
		}
		mountFilter.Path = rest
	}
	mountFilter.Mount = ""
	return mountFilter, true
}

func namespaceTask(mount string, item task.Task) task.Task {
	item.Mount = mount
	item.ID = namespaceTaskID(mount, item.ID)
	item.Path = joinVirtual("/"+mount, strings.TrimPrefix(item.Path, "/"))
	return item
}

func splitNamespacePath(path string) (string, string, bool) {
	path = cleanVirtual(path)
	if path == "/" {
		return "", "", true
	}
	trimmed := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 1 {
		return parts[0], "/", false
	}
	return parts[0], "/" + parts[1], false
}

func namespaceTaskID(mount, id string) string {
	if mount == "" || id == "" {
		return id
	}
	return mount + ":" + id
}

func splitNamespaceTaskID(id string) (string, string, bool) {
	mount, local, ok := strings.Cut(id, ":")
	if !ok || mount == "" || local == "" {
		return "", "", false
	}
	return mount, local, true
}

func (n *Namespace) CancelTask(ctx context.Context, id string) error {
	return n.applyTaskAction(ctx, id, func(fs *VFS, localID string) error {
		return fs.CancelTask(ctx, localID)
	})
}

func (n *Namespace) RetryTask(ctx context.Context, id string) error {
	return n.applyTaskAction(ctx, id, func(fs *VFS, localID string) error {
		return fs.RetryTask(ctx, localID)
	})
}

func (n *Namespace) applyTaskAction(ctx context.Context, id string, fn func(*VFS, string) error) error {
	if mountName, localID, ok := splitNamespaceTaskID(id); ok {
		n.mu.RLock()
		fs := n.mounts[mountName]
		n.mu.RUnlock()
		if fs == nil {
			return task.ErrNotFound
		}
		return fn(fs, localID)
	}
	n.mu.RLock()
	mounts := make([]*VFS, 0, len(n.mounts))
	for _, fs := range n.mounts {
		mounts = append(mounts, fs)
	}
	n.mu.RUnlock()
	for _, fs := range mounts {
		if err := fn(fs, id); err == nil {
			return nil
		} else if !isTaskNotFound(err) {
			return err
		}
	}
	return task.ErrNotFound
}

// MountSpace pairs a mount name with its space usage. Err is set when the
// underlying driver does not support space queries.
type MountSpace struct {
	Name  string
	Space drive.Space
	Err   error
}

// MountSpaces reports space usage for every mount individually, sorted by
// mount name. Unlike Space (which aggregates), this lets callers show a
// per-mount breakdown.
func (n *Namespace) MountSpaces(ctx context.Context) []MountSpace {
	n.mu.RLock()
	mounts := make([]*VFS, 0, len(n.mounts))
	names := make([]string, 0, len(n.mounts))
	for name, mount := range n.mounts {
		mounts = append(mounts, mount)
		names = append(names, name)
	}
	n.mu.RUnlock()

	results := make([]MountSpace, 0, len(mounts))
	for i, mount := range mounts {
		space, err := mount.Space(ctx)
		results = append(results, MountSpace{Name: names[i], Space: space, Err: err})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results
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
			ID:        "/" + name,
			ParentID:  "/",
			Name:      name,
			IsDir:     true,
			ModTime:   n.createdAt,
			CreatedAt: n.createdAt,
			UpdatedAt: n.createdAt,
		})
	}
	return entries
}

func cleanMountName(name string) string {
	return strings.Trim(strings.TrimSpace(name), "/")
}

var _ FileSystem = (*VFS)(nil)
var _ FileSystem = (*Namespace)(nil)
var _ Reader = (*VFS)(nil)
var _ Writer = (*VFS)(nil)
var _ Lifecycle = (*VFS)(nil)
var _ PathRefresher = (*VFS)(nil)
var _ Lifecycle = (*Namespace)(nil)
var _ PathRefresher = (*Namespace)(nil)
