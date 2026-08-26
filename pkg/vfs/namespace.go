package vfs

import (
	"context"
	"errors"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
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

// RawReader reads the backend bytes for a virtual path without higher-level
// transforms such as crypt decryption or read-cache staging.
type RawReader interface {
	ReadRaw(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error)
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

// Lifecycle starts and stops a filesystem's background workers. It is
// intentionally separate from FileSystem: constructing a filesystem
// (New/NewNamespace) does not run anything, and read-only consumers never
// need to start one.
//
// Ownership: the first context passed to Start owns the filesystem's
// lifecycle; later calls are no-ops and the instance stops when that
// context is cancelled (which triggers Close) or when Close is called
// explicitly. A stopped instance is not restartable - build a new one.
// Close is idempotent and safe to call before Start.
type Lifecycle interface {
	Start(ctx context.Context)
	Close(ctx context.Context) error
}

// PathRefresher invalidates the directory listing cache for path so the
// next List call fetches fresh data from the remote driver. Cache/view
// control is an optional capability, not a file operation.
type PathRefresher interface {
	RefreshPath(path string)
}

// InvalidationSource publishes paths whose cached kernel view is stale after
// a mutation completed outside the originating kernel request, such as an
// asynchronous upload. Synchronous FUSE operations already update the kernel
// view as part of their response. Subscribers must return promptly. The
// returned function removes the subscription and is safe to call repeatedly.
type InvalidationSource interface {
	SubscribeInvalidations(func(path string)) func()
}

// ListPager pages a directory listing with a cursor; a vfs-owned optional
// consumer surface, gated by CapabilityListPage.
type ListPager interface {
	ListPage(ctx context.Context, path, cursor string, limit int) (ListPageResult, error)
}

// ReadCacheCloser drops the read-cache files; a vfs-owned optional consumer
// surface, gated by CapabilityCloseReadCache.
type ReadCacheCloser interface {
	CloseReadCache() error
}

// ReadSessionReleaser forgets adaptive read hints for a closed open-file
// handle; a vfs-owned optional consumer surface, gated by
// CapabilityReleaseReadSession.
type ReadSessionReleaser interface {
	ReleaseReadSession(sessionID uint64)
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

type SourceUploadRequest struct {
	Source   drive.ReadOnlyFileSource
	Progress drive.UploadProgress
	ModTime  time.Time
}

type SourceUploader interface {
	SupportsSourceUpload(path string) bool
	SupportsResumableSourceUpload(path string) bool
	UploadSource(ctx context.Context, path string, req SourceUploadRequest) (drive.Entry, error)
}

// Optional public capability interfaces are grouped by consumer role:
//
//	file operations  FileSystem (Reader + Writer), Lifecycle, PathRefresher,
//	                 InvalidationSource
//	runtime caps     UploadInspector, RemoteLister, HashProvider,
//	                 EncryptedHashProvider, ModTimeWriter, SpaceProvider,
//	                 MountSpaceProvider
//	test control    DebugUploadCancelInjector (in debug_fault.go)
//
// Diagnostics-only capabilities live in pkg/vfs/diagnostics so they do
// not become part of the stable filesystem API.

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

// ReadCacheCleaner drops the read-cache files.
type ReadCacheCleaner interface {
	ClearReadCache() error
}

// MountReadCacheCleaner drops read-cache entries for one named mount.
type MountReadCacheCleaner interface {
	ClearReadCacheForMount(name string) error
}

// ReadCacheFlusher flushes pending read-cache writes.
type ReadCacheFlusher interface {
	FlushReadCache() error
}

// MountedFileSystem is the surface a namespace mount must provide: file
// operations, lifecycle, path refresh, and task-source introspection. It is
// the contract shared by every filesystem constructor (single VFS or
// Namespace); consumers such as pkg/core type their filesystem API on it
// directly instead of on the concrete *VFS.
type MountedFileSystem interface {
	FileSystem
	Lifecycle
	PathRefresher
	TaskSource() task.Source
}

type Mount struct {
	Name string
	FS   MountedFileSystem
}

// mountAsVFS asserts a mount's filesystem is a VFS instance. A Namespace is
// a VFS aggregator: its shared state (view, overlay, per-mount debug
// internals) is reached through the concrete type, so only VFS instances
// can be mounted.
func mountAsVFS(name string, fs MountedFileSystem) (*VFS, error) {
	if fs == nil {
		return nil, fmt.Errorf("vfs: mount %s: nil filesystem", name)
	}
	v, ok := fs.(*VFS)
	if !ok {
		return nil, fmt.Errorf("vfs: mount %s: filesystem is not a VFS instance", name)
	}
	return v, nil
}

// Namespace mounts multiple VFS instances under one virtual root. The first
// path segment is the mount name: /quark/docs, /quark2/docs, /localfs/docs.
type Namespace struct {
	mu     sync.RWMutex
	mounts map[string]*VFS
	// subs tracks live invalidation subscriptions so AddMount/RemoveMount
	// can attach and detach mounts without breaking the listener contract.
	subs      map[*invalidationSubscription]struct{}
	createdAt time.Time
}

func NewNamespace(mounts []Mount) (*Namespace, error) {
	ns := &Namespace{mounts: map[string]*VFS{}, subs: map[*invalidationSubscription]struct{}{}, createdAt: time.Now()}
	for _, mount := range mounts {
		name := vfstypes.CleanMountName(mount.Name)
		if name == "" {
			return nil, fmt.Errorf("vfs: mount name required")
		}
		if _, exists := ns.mounts[name]; exists {
			return nil, fmt.Errorf("vfs: duplicate mount name %q", name)
		}
		fs, err := mountAsVFS(name, mount.FS)
		if err != nil {
			return nil, err
		}
		ns.mounts[name] = fs
	}
	return ns, nil
}

// AddMount mounts an additional VFS instance under the namespace root. The
// mount becomes visible to every namespace operation immediately. Lifecycle
// is the caller's: a mount added after Namespace.Start is not started here
// (call mount.FS.Start yourself with the shareable context), and
// RemoveMount does not Close. The invalidation subscriptions are extended to
// the new mount so its async uploads keep reaching subscribers.
func (n *Namespace) AddMount(mount Mount) error {
	name := vfstypes.CleanMountName(mount.Name)
	if name == "" {
		return fmt.Errorf("vfs: mount name required")
	}
	fs, err := mountAsVFS(name, mount.FS)
	if err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, exists := n.mounts[name]; exists {
		return fmt.Errorf("vfs: duplicate mount name %q", name)
	}
	n.mounts[name] = fs
	for sub := range n.subs {
		sub.attach(name, fs)
	}
	return nil
}

// RemoveMount detaches a mounted filesystem from the namespace. It does not
// close or stop the filesystem - the caller owns that lifecycle. Pending
// uploads, tasks, and invalidation subscriptions of the removed mount are
// dropped with it. Removing an unknown mount is an error.
func (n *Namespace) RemoveMount(name string) error {
	name = vfstypes.CleanMountName(name)
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, exists := n.mounts[name]; !exists {
		return fmt.Errorf("vfs: unknown mount %q", name)
	}
	delete(n.mounts, name)
	for sub := range n.subs {
		sub.detach(name)
	}
	return nil
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

// Close shuts down every mounted filesystem and waits for each one's
// background workers to finish. It snapshots the mounts under the read
// lock, so mounts added concurrently after Close starts are not closed
// (mirroring Start's snapshot semantics). Mounts are closed concurrently:
// every mount gets the full ctx budget, so a slow or timing-out mount
// cannot starve the others' teardown or mask their errors. A failing mount
// does not prevent the remaining mounts from closing. Close is idempotent
// because each mount's Close is idempotent.
func (n *Namespace) Close(ctx context.Context) error {
	n.mu.RLock()
	mounts := make([]*VFS, 0, len(n.mounts))
	for _, fs := range n.mounts {
		mounts = append(mounts, fs)
	}
	n.mu.RUnlock()
	errCh := make(chan error, len(mounts))
	var wg sync.WaitGroup
	for _, fs := range mounts {
		wg.Add(1)
		go func(fs *VFS) {
			defer wg.Done()
			errCh <- fs.Close(ctx)
		}(fs)
	}
	wg.Wait()
	close(errCh)
	var errs []error
	for err := range errCh {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func splitNamespacePath(path string) (string, string, bool) {
	path = vfstypes.CleanVirtualPath(path)
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

// firstVirtualSegment returns the first slash-delimited segment of a
// virtual path ("" for root).
func firstVirtualSegment(path string) string {
	trimmed := strings.TrimPrefix(vfstypes.CleanVirtualPath(path), "/")
	name, _, _ := strings.Cut(trimmed, "/")
	return name
}

func (n *Namespace) resolve(path string) (*VFS, string, bool, error) {
	name, rest, root := splitNamespacePath(path)
	if root {
		return nil, "/", true, nil
	}
	name = vfstypes.CleanMountName(name)
	n.mu.RLock()
	mount := n.mounts[name]
	n.mu.RUnlock()
	if mount == nil {
		return nil, "", false, fmt.Errorf("%w: unknown mount %q", ErrNotFound, name)
	}
	return mount, rest, false, nil
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

var _ FileSystem = (*VFS)(nil)
var _ FileSystem = (*Namespace)(nil)
var _ Reader = (*VFS)(nil)
var _ Writer = (*VFS)(nil)
var _ Lifecycle = (*VFS)(nil)
var _ PathRefresher = (*VFS)(nil)
var _ Lifecycle = (*Namespace)(nil)
var _ PathRefresher = (*Namespace)(nil)
var _ InvalidationSource = (*VFS)(nil)
var _ InvalidationSource = (*Namespace)(nil)
var _ MountedFileSystem = (*VFS)(nil)
var _ MountedFileSystem = (*Namespace)(nil)
var _ Capabler = (*VFS)(nil)
var _ Capabler = (*Namespace)(nil)
