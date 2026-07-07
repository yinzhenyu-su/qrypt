package mount

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/winfsp/cgofuse/fuse"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type adapter struct {
	fuse.FileSystemBase
	fs                  vfs.FileSystem
	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelFunc
	handles             map[uint64]fuseHandle
	ignoredApple        map[string]ignoredAppleNode
	activeOps           map[uint64]activeFuseOp
	nextFH              uint64
	nextOp              uint64
	stopping            bool
	trace               fuseTracer
	statfs              StatfsOptions
	readOnly            bool
	ignoreAppleMetadata bool
	ignoreAppleXattr    bool
}

type ignoredAppleNode struct {
	isDir bool
	size  int64
	mtime time.Time
}

type fuseHandle struct {
	path     string
	entry    drive.Entry
	hasEntry bool
}

type activeFuseOp struct {
	Op    string
	Path  string
	Start time.Time
}

type fuseTracer struct{}

func (fuseTracer) log(op, path, format string, args ...any) {
	msg := fmt.Sprintf("[FUSE] %s path=%q %s", op, path, fmt.Sprintf(format, args...))
	if strings.Contains(msg, "err=") && !strings.Contains(msg, "err=0") && !strings.Contains(msg, "err=-2") && !strings.Contains(msg, "err=-93") {
		if fuseErrorTraceSuppressed(op, path) {
			logging.L.Debugf("%s", msg)
			return
		}
		logging.L.Warnf("%s", msg)
		return
	}
	if strings.Contains(msg, " result=-") {
		if fuseErrorTraceSuppressed(op, path) {
			logging.L.Debugf("%s", msg)
			return
		}
		logging.L.Warnf("%s", msg)
		return
	}
	logging.L.DebugfEvery("fuse.trace."+op, time.Second, "%s", msg)
}

var fuseErrorTraceSuppress sync.Map

func suppressFuseErrorTrace(op, path string) {
	fuseErrorTraceSuppress.Store(op+"\x00"+path, time.Now().Add(time.Second))
}

func fuseErrorTraceSuppressed(op, path string) bool {
	key := op + "\x00" + path
	value, ok := fuseErrorTraceSuppress.Load(key)
	if !ok {
		return false
	}
	deadline, ok := value.(time.Time)
	if !ok || time.Now().After(deadline) {
		fuseErrorTraceSuppress.Delete(key)
		return false
	}
	return true
}

type readOnlyPathChecker interface {
	IsReadOnlyPath(path string) bool
}

type directoryCopyPreparer interface {
	PrepareDirectoryCopy(ctx context.Context, path string) error
}

type modTimeSetter interface {
	SetModTime(ctx context.Context, path string, modTime time.Time) error
}

type StatfsOptions struct {
	TotalSpace int64
	FreeSpace  int64
}

func (s StatfsOptions) withDefaults() StatfsOptions {
	if s.TotalSpace <= 0 {
		s.TotalSpace = 512 << 30
	}
	if s.FreeSpace <= 0 {
		s.FreeSpace = 400 << 30
	}
	if s.FreeSpace > s.TotalSpace {
		s.FreeSpace = s.TotalSpace
	}
	return s
}

func newAdapter(fs vfs.FileSystem, statfs StatfsOptions) *adapter {
	return newAdapterWithOptions(fs, adapterOptions{Statfs: statfs})
}

type adapterOptions struct {
	Statfs              StatfsOptions
	ReadOnly            bool
	IgnoreAppleMetadata bool
	IgnoreAppleXattr    bool
}

func newAdapterWithOptions(fs vfs.FileSystem, opts adapterOptions) *adapter {
	ctx, cancel := context.WithCancel(context.Background())
	return &adapter{
		fs:                  fs,
		ctx:                 ctx,
		cancel:              cancel,
		handles:             map[uint64]fuseHandle{},
		ignoredApple:        map[string]ignoredAppleNode{},
		activeOps:           map[uint64]activeFuseOp{},
		trace:               fuseTracer{},
		statfs:              opts.Statfs,
		readOnly:            opts.ReadOnly,
		ignoreAppleMetadata: opts.IgnoreAppleMetadata,
		ignoreAppleXattr:    opts.IgnoreAppleXattr,
	}
}

func (a *adapter) shutdown() {
	a.mu.Lock()
	if a.stopping {
		a.mu.Unlock()
		return
	}
	a.stopping = true
	if a.cancel != nil {
		a.cancel()
	}
	active := a.activeOpsSnapshotLocked()
	a.mu.Unlock()
	if len(active) > 0 {
		logging.L.Infof("[FUSE] shutdown requested with active operations count=%d ops=%s", len(active), formatActiveFuseOps(active))
	}
}

func (a *adapter) Init() {
	logging.L.Infof("[FUSE] Init pid=%d", os.Getpid())
}

func (a *adapter) Destroy() {
	logging.L.Infof("[FUSE] Destroy pid=%d", os.Getpid())
}

func (a *adapter) isStopping() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopping
}

func (a *adapter) beginOp(op, path string) (context.Context, func(), bool) {
	a.mu.Lock()
	if a.stopping {
		a.mu.Unlock()
		return context.Background(), func() {}, false
	}
	a.nextOp++
	id := a.nextOp
	a.activeOps[id] = activeFuseOp{Op: op, Path: path, Start: time.Now()}
	ctx := a.ctx
	a.mu.Unlock()
	return ctx, func() {
		a.mu.Lock()
		delete(a.activeOps, id)
		a.mu.Unlock()
	}, true
}

func (a *adapter) activeOpsSnapshot() []activeFuseOp {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.activeOpsSnapshotLocked()
}

func (a *adapter) activeOpsSnapshotLocked() []activeFuseOp {
	ops := make([]activeFuseOp, 0, len(a.activeOps))
	for _, op := range a.activeOps {
		ops = append(ops, op)
	}
	return ops
}

func formatActiveFuseOps(ops []activeFuseOp) string {
	if len(ops) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(ops))
	now := time.Now()
	for _, op := range ops {
		parts = append(parts, fmt.Sprintf("%s:%s:%s", op.Op, op.Path, now.Sub(op.Start).Round(time.Millisecond)))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func (a *adapter) nextHandle(path string, entries ...drive.Entry) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextFH++
	handle := fuseHandle{path: path}
	if len(entries) > 0 {
		handle.entry = entries[0]
		handle.hasEntry = true
	}
	a.handles[a.nextFH] = handle
	return a.nextFH
}

func (a *adapter) handleEntry(fh uint64) (drive.Entry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	handle, ok := a.handles[fh]
	if !ok || !handle.hasEntry {
		return drive.Entry{}, false
	}
	return handle.entry, true
}

func (a *adapter) releaseHandle(fh uint64) {
	a.mu.Lock()
	delete(a.handles, fh)
	a.mu.Unlock()
}

func (a *adapter) effectiveStatfs() StatfsOptions {
	space := a.statfs
	if space.TotalSpace <= 0 || space.FreeSpace <= 0 {
		if querier, ok := a.fs.(drive.SpaceQuerier); ok {
			if auto, err := querier.Space(context.Background()); err == nil {
				if space.TotalSpace <= 0 {
					space.TotalSpace = auto.Total
				}
				if space.FreeSpace <= 0 {
					space.FreeSpace = auto.Free
				}
			}
		}
	}
	return space.withDefaults()
}

func blocksForBytes(bytes int64, blockSize uint64) uint64 {
	if bytes <= 0 || blockSize == 0 {
		return 0
	}
	return uint64((bytes + int64(blockSize) - 1) / int64(blockSize))
}

func fillStat(stat *fuse.Stat_t, entry drive.Entry, fallbackPath ...string) {
	uid, gid, _ := fuse.Getcontext()
	stat.Uid = uid
	stat.Gid = gid
	if entry.IsDir {
		stat.Mode = fuse.S_IFDIR | 0o755
		stat.Nlink = 2
	} else {
		stat.Mode = fuse.S_IFREG | 0o644
		stat.Nlink = 1
		stat.Size = entry.Size
		stat.Blocks = (entry.Size + 511) / 512
	}
	path := ""
	if len(fallbackPath) > 0 {
		path = fallbackPath[0]
	}
	stat.Ino = stableInode(entry, path)
	stat.Blksize = 4096
	if entry.ModTime.IsZero() {
		entry.ModTime = time.Now()
	}
	stat.Atim = fuse.NewTimespec(entry.ModTime)
	stat.Mtim = stat.Atim
	stat.Ctim = stat.Atim
	stat.Birthtim = stat.Atim
}

func (a *adapter) isReadOnlyPath(path string) bool {
	if a.readOnly {
		return true
	}
	checker, ok := a.fs.(readOnlyPathChecker)
	return ok && checker.IsReadOnlyPath(path)
}

func fuseErr(err error) int {
	if errors.Is(err, vfs.ErrReadOnly) {
		return -fuse.EROFS
	}
	if errors.Is(err, vfs.ErrNotFound) {
		return -fuse.ENOENT
	}
	return -fuse.EIO
}

func (a *adapter) removeXattrs(path string)             {}
func (a *adapter) renameXattrs(oldPath, newPath string) {}

func stableInode(entry drive.Entry, fallbackPath string) uint64 {
	h := fnv.New64a()
	key := entry.ID
	if key == "" {
		key = fallbackPath
	}
	if key == "" {
		key = entry.Name
	}
	h.Write([]byte(key))
	return h.Sum64()
}

func logFuseResult(op, path string, start time.Time, errc *int) {
	if errc == nil {
		return
	}
	elapsed := time.Since(start)
	if *errc != 0 && *errc != -fuse.ENOENT {
		logging.L.Warnf("[FUSE] %s path=%q errc=%d took=%v", op, path, *errc, elapsed)
		return
	}
	if elapsed > 100*time.Millisecond {
		logging.L.WarnfEvery("fuse.slow."+op, time.Second, "[FUSE] %s path=%q errc=%d took=%v (slow)", op, path, *errc, elapsed)
		return
	}
	logging.L.DebugfEvery("fuse.result."+op, time.Second, "[FUSE] %s path=%q errc=%d took=%v", op, path, *errc, elapsed)
}

func logFuseError(op, path string, errc int, err error) {
	if err == nil {
		return
	}
	if errc == -fuse.ENOENT {
		logging.L.DebugfEvery("fuse.enoent."+op, time.Second, "[FUSE] %s path=%q errc=%d error=%v", op, path, errc, err)
		return
	}
	suppressFuseErrorTrace(op, path)
	logging.L.Warnf("[FUSE] %s path=%q errc=%d error=%v", op, path, errc, err)
}

func logFuseAttrResult(path string, stat *fuse.Stat_t, entry drive.Entry) {
	logging.L.DebugfEvery("fuse.attr", time.Second, "[FUSE] GetattrResult path=%q ino=%d mode=%o size=%d dir=%t", path, stat.Ino, stat.Mode, stat.Size, entry.IsDir)
}
