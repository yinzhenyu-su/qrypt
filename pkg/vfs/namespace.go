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
//
// Ownership: the first context passed to Start owns the filesystem's
// lifecycle; later calls are no-ops and the instance stops when that
// context is cancelled. A cancelled instance is not restartable - build a
// new one.
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

// Start propagates the lifecycle context to every mounted filesystem. Each
// mount receives the same ctx; per the Lifecycle contract the FIRST Start on
// any mount owns that mount's lifecycle, so repeated Namespace.Start calls
// (or a mount that was already started directly) are no-ops. Namespace.Start
// only propagates - it never replaces or re-owns a mount's lifecycle, and
// there is no way to stop one mount without cancelling the shared context.
func (n *Namespace) Start(ctx context.Context) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, fs := range n.mounts {
		fs.Start(ctx)
	}
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
