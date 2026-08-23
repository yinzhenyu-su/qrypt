package mount

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/winfsp/cgofuse/fuse"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	vfsread "github.com/yinzhenyu/qrypt/pkg/vfs/read"
)

type adapter struct {
	fuse.FileSystemBase
	fs                  vfs.FileSystem
	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelFunc
	handles             map[uint64]fuseHandle
	ignoredApple        map[string]ignoredAppleNode
	xattrs              map[string]map[string][]byte
	activeOps           [activeOpsSlots]activeOpsSlot
	nextFH              uint64
	nextOp              atomic.Uint64
	stopping            atomic.Bool
	ready               chan struct{}
	readyOnce           sync.Once
	trace               fuseTracer
	statfs              StatfsOptions
	statfsAuto          statfsAutoCache
	readOnly            bool
	allowOther          bool
	credsOnce           sync.Once
	cachedUID           uint32
	cachedGID           uint32
	ignoreAppleMetadata bool
	delegateAppleXattr  bool
}

// activeOpsSlots is the fixed capacity of the per-op active-operation ring.
// FUSE ops are short-lived, so a few hundred concurrent ops is a generous
// bound; when full, beginOp returns false (op runs untracked).
const activeOpsSlots = 256

type activeOpsSlot struct {
	id atomic.Uint64 // 0 = empty, otherwise the op id occupying the slot
	mu sync.RWMutex
	op activeFuseOp
}

type ignoredAppleNode struct {
	isDir bool
	size  int64
	data  []byte
	mtime time.Time
}

type fuseHandle struct {
	path          string
	flags         int
	entry         drive.Entry
	hasEntry      bool
	readSessionID uint64
	readRequests  uint64
	activeReads   int
	released      bool
}

var nextReadSessionID atomic.Uint64

type activeFuseOp struct {
	Op    string
	Path  string
	Start time.Time
}

type fuseTracer struct{}

func (fuseTracer) log(op, path, format string, args ...any) {
	// Hot path: every FUSE op builds and sanitizes this message. Skip the
	// work entirely unless debug logging is enabled; real errors are logged
	// separately via logFuseError regardless of level.
	if !logging.L.Enabled(logging.LevelDebug) {
		return
	}
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

const statfsAutoSpaceTTL = 60 * time.Second

type statfsAutoCache struct {
	space      drive.Space
	expiresAt  time.Time
	refreshing bool
}

const statfsAutoRetryDelay = 5 * time.Second

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
	AllowOther          bool
	IgnoreAppleMetadata bool
	DelegateAppleXattr  bool
}

func newAdapterWithOptions(fs vfs.FileSystem, opts adapterOptions) *adapter {
	ctx, cancel := context.WithCancel(context.Background())
	return &adapter{
		fs:                  fs,
		ctx:                 ctx,
		cancel:              cancel,
		ready:               make(chan struct{}),
		handles:             map[uint64]fuseHandle{},
		ignoredApple:        map[string]ignoredAppleNode{},
		xattrs:              map[string]map[string][]byte{},
		trace:               fuseTracer{},
		statfs:              opts.Statfs,
		statfsAuto:          statfsAutoCache{},
		readOnly:            opts.ReadOnly,
		allowOther:          opts.AllowOther,
		ignoreAppleMetadata: opts.IgnoreAppleMetadata,
		delegateAppleXattr:  opts.DelegateAppleXattr,
	}
}

func (a *adapter) shutdown() {
	a.mu.Lock()
	if a.stopping.Load() {
		a.mu.Unlock()
		return
	}
	a.stopping.Store(true)
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
	a.readyOnce.Do(func() { close(a.ready) })
}

func (a *adapter) Destroy() {
	logging.L.Infof("[FUSE] Destroy pid=%d", os.Getpid())
}

func (a *adapter) beginOp(op, path string) (context.Context, func(), bool) {
	if a.stopping.Load() {
		return context.Background(), func() {}, false
	}
	now := time.Now()
	id := a.nextOp.Add(1)
	for i := 0; i < activeOpsSlots; i++ {
		slot := &a.activeOps[id%activeOpsSlots]
		slot.mu.Lock()
		if slot.id.Load() == 0 {
			slot.op = activeFuseOp{Op: op, Path: path, Start: now}
			slot.id.Store(id)
			slot.mu.Unlock()
			return a.ctx, func() {
				slot.mu.Lock()
				if slot.id.Load() == id {
					slot.op = activeFuseOp{}
					slot.id.Store(0)
				}
				slot.mu.Unlock()
			}, true
		}
		slot.mu.Unlock()
		id++
	}
	// All slots busy: run the op untracked.
	return context.Background(), func() {}, false
}

func (a *adapter) activeOpsSnapshot() []activeFuseOp {
	return a.activeOpsSnapshotLocked()
}

func (a *adapter) activeOpsSnapshotLocked() []activeFuseOp {
	ops := make([]activeFuseOp, 0, activeOpsSlots)
	for i := range a.activeOps {
		slot := &a.activeOps[i]
		if slot.id.Load() == 0 {
			continue
		}
		slot.mu.RLock()
		ops = append(ops, slot.op)
		slot.mu.RUnlock()
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
	return a.nextHandleWithFlags(path, 0, entries...)
}

func (a *adapter) nextHandleWithFlags(path string, flags int, entries ...drive.Entry) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextFH++
	handle := fuseHandle{path: path, flags: flags}
	if len(entries) > 0 {
		handle.entry = entries[0]
		handle.hasEntry = true
	}
	a.handles[a.nextFH] = handle
	return a.nextFH
}

func (a *adapter) handleWritable(fh uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	handle, ok := a.handles[fh]
	if !ok {
		return fh == 0
	}
	return handle.flags&3 != 0
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

func (a *adapter) beginHandleRead(fh uint64) (vfsread.AccessHint, func() uint64) {
	if fh == 0 {
		return vfsread.AccessHint{}, func() uint64 { return 0 }
	}
	a.mu.Lock()
	handle, ok := a.handles[fh]
	if !ok || handle.released {
		a.mu.Unlock()
		return vfsread.AccessHint{}, func() uint64 { return 0 }
	}
	if handle.readSessionID == 0 {
		handle.readSessionID = nextReadSessionID.Add(1)
	}
	handle.readRequests++
	handle.activeReads++
	hint := vfsread.AccessHint{
		SessionID:  handle.readSessionID,
		RequestID:  handle.readRequests,
		Concurrent: handle.activeReads > 1,
	}
	a.handles[fh] = handle
	a.mu.Unlock()
	return hint, func() uint64 {
		a.mu.Lock()
		defer a.mu.Unlock()
		current, ok := a.handles[fh]
		if !ok || current.readSessionID != hint.SessionID {
			return 0
		}
		if current.activeReads > 0 {
			current.activeReads--
		}
		if current.released && current.activeReads == 0 {
			delete(a.handles, fh)
			return current.readSessionID
		}
		a.handles[fh] = current
		return 0
	}
}

func (a *adapter) releaseHandle(fh uint64) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	handle, ok := a.handles[fh]
	if !ok {
		return 0
	}
	if handle.activeReads > 0 {
		handle.released = true
		a.handles[fh] = handle
		return 0
	}
	delete(a.handles, fh)
	return handle.readSessionID
}

func (a *adapter) releaseReadSession(sessionID uint64) {
	if sessionID == 0 {
		return
	}
	if releaser, ok := a.fs.(interface{ ReleaseReadSession(uint64) }); ok {
		releaser.ReleaseReadSession(sessionID)
	}
}

func (a *adapter) effectiveStatfs() StatfsOptions {
	space := a.statfs
	if space.TotalSpace <= 0 || space.FreeSpace <= 0 {
		if auto, ok := a.autoStatfsSpace(); ok {
			if space.TotalSpace <= 0 {
				space.TotalSpace = auto.Total
			}
			if space.FreeSpace <= 0 {
				space.FreeSpace = auto.Free
			}
		}
	}
	return space.withDefaults()
}

func (a *adapter) autoStatfsSpace() (drive.Space, bool) {
	querier, ok := a.fs.(vfs.SpaceProvider)
	if !ok {
		return drive.Space{}, false
	}
	now := time.Now()
	a.mu.Lock()
	space := a.statfsAuto.space
	if now.Before(a.statfsAuto.expiresAt) {
		a.mu.Unlock()
		return space, space.Total > 0 || space.Free > 0
	}
	if !a.statfsAuto.refreshing {
		a.statfsAuto.refreshing = true
		go a.refreshAutoStatfsSpace(querier)
	}
	a.mu.Unlock()
	return space, space.Total > 0 || space.Free > 0
}

func (a *adapter) refreshAutoStatfsSpace(querier vfs.SpaceProvider) {
	space, err := querier.Space(a.ctx)
	now := time.Now()
	a.mu.Lock()
	a.statfsAuto.refreshing = false
	if err == nil {
		a.statfsAuto.space = space
		a.statfsAuto.expiresAt = now.Add(statfsAutoSpaceTTL)
	} else {
		a.statfsAuto.expiresAt = now.Add(statfsAutoRetryDelay)
	}
	a.mu.Unlock()
}

func blocksForBytes(bytes int64, blockSize uint64) uint64 {
	if bytes <= 0 || blockSize == 0 {
		return 0
	}
	return uint64((bytes + int64(blockSize) - 1) / int64(blockSize))
}

func fillStat(stat *fuse.Stat_t, entry drive.Entry, fallbackPath ...string) {
	fillStatWithCreds(stat, entry, 0, 0, false, fallbackPath...)
}

// fillStatWithCreds fills stat. When cacheCreds is set, uid/gid come from the
// provided cached values instead of fuse.Getcontext. fuse.Getcontext returns
// per-request caller credentials, so caching is only valid when allow_other is
// off (single-user mounts).
func fillStatWithCreds(stat *fuse.Stat_t, entry drive.Entry, cachedUID, cachedGID uint32, cacheCreds bool, fallbackPath ...string) {
	if cacheCreds {
		stat.Uid = cachedUID
		stat.Gid = cachedGID
	} else {
		uid, gid, _ := fuse.Getcontext()
		stat.Uid = uid
		stat.Gid = gid
	}
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

// fillStat fills stat with adapter-owned attributes. For single-user mounts
// (allow_other off) the caller credentials are constant, so fuse.Getcontext
// is resolved once instead of per stat/readdir entry.
func (a *adapter) fillStat(stat *fuse.Stat_t, entry drive.Entry, fallbackPath ...string) {
	if a.allowOther {
		fillStat(stat, entry, fallbackPath...)
		return
	}
	a.credsOnce.Do(func() {
		uid, gid, _ := fuse.Getcontext()
		a.cachedUID = uid
		a.cachedGID = gid
	})
	fillStatWithCreds(stat, entry, a.cachedUID, a.cachedGID, true, fallbackPath...)
}

func fuseErr(err error) int {
	if errors.Is(err, vfs.ErrReadOnly) {
		return -fuse.EROFS
	}
	if errors.Is(err, vfs.ErrNotFound) {
		return -fuse.ENOENT
	}
	if errors.Is(err, vfs.ErrCrossMount) {
		return -fuse.EXDEV
	}
	return -fuse.EIO
}

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
	if !logging.L.Enabled(logging.LevelDebug) {
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
	if !logging.L.Enabled(logging.LevelDebug) {
		return
	}
	logging.L.DebugfEvery("fuse.attr", time.Second, "[FUSE] GetattrResult path=%q ino=%d mode=%o size=%d dir=%t", path, stat.Ino, stat.Mode, stat.Size, entry.IsDir)
}
