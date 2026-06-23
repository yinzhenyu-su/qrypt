package mount

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/winfsp/cgofuse/fuse"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type Options struct {
	MountPoint     string
	ReadOnly       bool
	AllowOther     bool
	VolumeName     string
	NoAppleDouble  bool
	TotalSpace     int64
	FreeSpace      int64
	TraceEnabled   bool
	TraceFile      string
	Foreground     bool
	ReadyTimeout   time.Duration
	UnmountOnError bool
}

type Session struct {
	ID         string
	MountPoint string
	host       *fuse.FileSystemHost
	adapter    *adapter
}

type Mounter interface {
	Mount(ctx context.Context, fs vfs.FileSystem, opts Options) (*Session, error)
	Unmount(ctx context.Context, session *Session) error
}

type FuseMounter struct{}

func NewMounter() Mounter {
	return FuseMounter{}
}

func (FuseMounter) Mount(ctx context.Context, fs vfs.FileSystem, opts Options) (*Session, error) {
	if fs == nil {
		return nil, fmt.Errorf("mount: filesystem is nil")
	}
	if opts.MountPoint == "" {
		return nil, fmt.Errorf("mount: mount point required")
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 5 * time.Second
	}
	if err := os.MkdirAll(opts.MountPoint, 0o755); err != nil {
		return nil, err
	}

	ad := newAdapter(fs, TraceOptions{Enabled: opts.TraceEnabled, File: opts.TraceFile}, StatfsOptions{
		TotalSpace: opts.TotalSpace,
		FreeSpace:  opts.FreeSpace,
	})
	host := fuse.NewFileSystemHost(ad)
	session := &Session{
		ID:         opts.MountPoint,
		MountPoint: opts.MountPoint,
		host:       host,
		adapter:    ad,
	}

	mountOpts := mountOptions(opts)
	if opts.Foreground {
		if ok := host.Mount(opts.MountPoint, mountOpts); !ok {
			return nil, fmt.Errorf("mount: failed to mount %s", opts.MountPoint)
		}
		return session, nil
	}

	result := make(chan bool, 1)
	go func() {
		result <- host.Mount(opts.MountPoint, mountOpts)
	}()

	select {
	case <-ctx.Done():
		host.Unmount()
		return nil, ctx.Err()
	case ok := <-result:
		if !ok {
			return nil, fmt.Errorf("mount: failed to mount %s", opts.MountPoint)
		}
		return session, nil
	case <-time.After(opts.ReadyTimeout):
		return session, nil
	}
}

func (FuseMounter) Unmount(ctx context.Context, session *Session) error {
	if session == nil {
		return nil
	}
	if session.adapter != nil {
		session.adapter.shutdown()
	}
	if session.host != nil {
		session.host.Unmount()
	}
	cmd := unmountCommand(session.MountPoint)
	if cmd == nil {
		return nil
	}
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func mountOptions(opts Options) []string {
	mode := "rw"
	if opts.ReadOnly {
		mode = "ro"
	}
	flags := []string{
		"-o", mode,
		"-o", "attr_timeout=10",
		"-o", "entry_timeout=10",
		"-o", "negative_timeout=0",
		"-o", "use_ino",
	}
	if runtime.GOOS == "darwin" {
		flags = append(flags,
			"-o", "defer_permissions",
			"-o", "fsname=qrypt",
			"-o", "subtype=qrypt",
			"-o", "local",
			"-o", "iosize=1048576",
		)
	}
	if opts.AllowOther {
		flags = append(flags, "-o", "allow_other")
	}
	if opts.VolumeName != "" {
		flags = append(flags, "-o", "volname="+opts.VolumeName)
	}
	if opts.NoAppleDouble {
		flags = append(flags, "-o", "noappledouble")
	}
	return flags
}

type adapter struct {
	fuse.FileSystemBase
	fs       vfs.FileSystem
	mu       sync.Mutex
	handles  map[uint64]string
	xattrs   map[string]map[string][]byte
	nextFH   uint64
	stopping bool
	trace    *traceLogger
	statfs   StatfsOptions
}

type readOnlyPathChecker interface {
	IsReadOnlyPath(path string) bool
}

type TraceOptions struct {
	Enabled bool
	File    string
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

func newAdapter(fs vfs.FileSystem, trace TraceOptions, statfs StatfsOptions) *adapter {
	return &adapter{
		fs:      fs,
		handles: map[uint64]string{},
		xattrs:  map[string]map[string][]byte{},
		trace:   newTraceLogger(trace),
		statfs:  statfs,
	}
}

func (a *adapter) shutdown() {
	a.mu.Lock()
	a.stopping = true
	a.mu.Unlock()
}

func (a *adapter) Init() {
	a.trace.log("Init", "/", "pid=%d", os.Getpid())
}

func (a *adapter) Destroy() {
	a.trace.log("Destroy", "/", "pid=%d", os.Getpid())
	if a.trace != nil {
		a.trace.close()
	}
}

func (a *adapter) isStopping() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopping
}

func (a *adapter) nextHandle(path string) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextFH++
	a.handles[a.nextFH] = path
	return a.nextFH
}

func (a *adapter) releaseHandle(fh uint64) {
	a.mu.Lock()
	delete(a.handles, fh)
	a.mu.Unlock()
}

func (a *adapter) Getattr(path string, stat *fuse.Stat_t, fh uint64) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Getattr", path, "fh=%d err=%d dur=%s", fh, errc, time.Since(start)) }()
	entry, err := a.fs.Stat(context.Background(), path)
	if err != nil {
		errc = -fuse.ENOENT
		return errc
	}
	fillStat(stat, entry)
	if a.isReadOnlyPath(path) {
		stat.Mode &^= 0o222
	}
	a.trace.log("GetattrResult", path, "ino=%d mode=%o size=%d dir=%t", stat.Ino, stat.Mode, stat.Size, entry.IsDir)
	return 0
}

func (a *adapter) Access(path string, mask uint32) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Access", path, "mask=%d err=%d dur=%s", mask, errc, time.Since(start)) }()
	if _, err := a.fs.Stat(context.Background(), path); err != nil {
		errc = -fuse.ENOENT
		return errc
	}
	return 0
}

func (a *adapter) Readdir(path string, fill func(name string, stat *fuse.Stat_t, ofst int64) bool, ofst int64, fh uint64) int {
	start := time.Now()
	errc := 0
	count := 0
	defer func() {
		a.trace.log("Readdir", path, "fh=%d ofst=%d count=%d err=%d dur=%s", fh, ofst, count, errc, time.Since(start))
	}()
	fill(".", nil, 0)
	fill("..", nil, 0)
	entries, err := a.fs.List(context.Background(), path)
	if err != nil {
		errc = -fuse.ENOENT
		return errc
	}
	for _, entry := range entries {
		st := &fuse.Stat_t{}
		fillStat(st, entry)
		if a.isReadOnlyPath(childPath(path, entry.Name)) {
			st.Mode &^= 0o222
		}
		count++
		if !fill(entry.Name, st, 0) {
			break
		}
	}
	return 0
}

func (a *adapter) Open(path string, flags int) (int, uint64) {
	start := time.Now()
	errc := 0
	var fh uint64
	defer func() { a.trace.log("Open", path, "flags=%d fh=%d err=%d dur=%s", flags, fh, errc, time.Since(start)) }()
	if _, err := a.fs.Stat(context.Background(), path); err != nil {
		errc = -fuse.ENOENT
		return errc, 0
	}
	fh = a.nextHandle(path)
	return 0, fh
}

func (a *adapter) Create(path string, flags int, mode uint32) (int, uint64) {
	start := time.Now()
	errc := 0
	var fh uint64
	defer func() {
		a.trace.log("Create", path, "flags=%d mode=%o fh=%d err=%d dur=%s", flags, mode, fh, errc, time.Since(start))
	}()
	if a.isStopping() {
		errc = -fuse.EIO
		return errc, 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc, 0
	}
	if err := a.fs.Create(context.Background(), path); err != nil {
		errc = fuseErr(err)
		return errc, 0
	}
	fh = a.nextHandle(path)
	return 0, fh
}

func (a *adapter) Read(path string, buff []byte, ofst int64, fh uint64) int {
	start := time.Now()
	result := 0
	defer func() {
		a.trace.log("Read", path, "fh=%d ofst=%d len=%d result=%d dur=%s", fh, ofst, len(buff), result, time.Since(start))
	}()
	rc, err := a.fs.Read(context.Background(), path, ofst, int64(len(buff)))
	if err != nil {
		result = -fuse.EIO
		return result
	}
	defer rc.Close()
	n, err := io.ReadFull(rc, buff)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		result = n
		return n
	}
	if err != nil {
		result = -fuse.EIO
		return result
	}
	if n == 0 {
		return 0
	}
	result = n
	return n
}

func (a *adapter) Write(path string, buff []byte, ofst int64, fh uint64) int {
	start := time.Now()
	result := 0
	defer func() {
		a.trace.log("Write", path, "fh=%d ofst=%d len=%d result=%d dur=%s", fh, ofst, len(buff), result, time.Since(start))
	}()
	if a.isStopping() {
		result = -fuse.EIO
		return result
	}
	if a.isReadOnlyPath(path) {
		result = -fuse.EROFS
		return result
	}
	n, err := a.fs.WriteAt(context.Background(), path, buff, ofst)
	if err != nil {
		result = fuseErr(err)
		return result
	}
	result = n
	return n
}

func (a *adapter) Flush(path string, fh uint64) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Flush", path, "fh=%d err=%d dur=%s", fh, errc, time.Since(start)) }()
	if err := a.fs.Flush(context.Background(), path); err != nil {
		errc = fuseErr(err)
		return errc
	}
	return 0
}

func (a *adapter) Fsync(path string, datasync bool, fh uint64) int {
	return a.Flush(path, fh)
}

func (a *adapter) Release(path string, fh uint64) int {
	start := time.Now()
	defer func() { a.trace.log("Release", path, "fh=%d dur=%s", fh, time.Since(start)) }()
	a.releaseHandle(fh)
	return 0
}

func (a *adapter) Mkdir(path string, mode uint32) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Mkdir", path, "mode=%o err=%d dur=%s", mode, errc, time.Since(start)) }()
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if _, err := a.fs.Mkdir(context.Background(), path); err != nil {
		errc = fuseErr(err)
		return errc
	}
	return 0
}

func (a *adapter) Unlink(path string) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Unlink", path, "err=%d dur=%s", errc, time.Since(start)) }()
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if err := a.fs.Remove(context.Background(), path); err != nil {
		errc = fuseErr(err)
		return errc
	}
	a.removeXattrs(path)
	return 0
}

func (a *adapter) Rmdir(path string) int {
	return a.Unlink(path)
}

func (a *adapter) Rename(oldPath, newPath string) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Rename", oldPath, "new=%q err=%d dur=%s", newPath, errc, time.Since(start)) }()
	if a.isReadOnlyPath(oldPath) || a.isReadOnlyPath(newPath) {
		errc = -fuse.EROFS
		return errc
	}
	if err := a.fs.Rename(context.Background(), oldPath, newPath); err != nil {
		errc = fuseErr(err)
		return errc
	}
	a.renameXattrs(oldPath, newPath)
	return 0
}

func (a *adapter) Truncate(path string, size int64, fh uint64) int {
	start := time.Now()
	errc := 0
	defer func() {
		a.trace.log("Truncate", path, "fh=%d size=%d err=%d dur=%s", fh, size, errc, time.Since(start))
	}()
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if err := a.fs.Truncate(context.Background(), path, size); err != nil {
		errc = fuseErr(err)
		return errc
	}
	return 0
}

func (a *adapter) Ftruncate(path string, size int64, fh uint64) int {
	return a.Truncate(path, size, fh)
}

func (a *adapter) Rename3(oldPath, newPath string, flags uint32) int {
	start := time.Now()
	errc := 0
	defer func() {
		a.trace.log("Rename3", oldPath, "new=%q flags=%d err=%d dur=%s", newPath, flags, errc, time.Since(start))
	}()
	if flags != 0 {
		errc = -fuse.ENOSYS
		return errc
	}
	errc = a.Rename(oldPath, newPath)
	return errc
}

func (a *adapter) Utimens(path string, tmsp []fuse.Timespec) int {
	errc := 0
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
	}
	a.trace.log("Utimens", path, "count=%d err=%d", len(tmsp), errc)
	return errc
}

func (a *adapter) Statfs(path string, stat *fuse.Statfs_t) int {
	start := time.Now()
	defer func() {
		a.trace.log("Statfs", path, "blocks=%d bfree=%d bavail=%d dur=%s", stat.Blocks, stat.Bfree, stat.Bavail, time.Since(start))
	}()
	space := a.effectiveStatfs()
	stat.Bsize = 4096
	stat.Frsize = 4096
	stat.Blocks = blocksForBytes(space.TotalSpace, stat.Bsize)
	stat.Bfree = blocksForBytes(space.FreeSpace, stat.Bsize)
	stat.Bavail = stat.Bfree
	stat.Namemax = 255
	return 0
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

func (a *adapter) Chmod(path string, mode uint32) int {
	if a.isReadOnlyPath(path) {
		return -fuse.EROFS
	}
	return 0
}

func (a *adapter) Chown(path string, uid uint32, gid uint32) int {
	if a.isReadOnlyPath(path) {
		return -fuse.EROFS
	}
	return 0
}

func (a *adapter) Getxattr(path string, name string) (int, []byte) {
	errc := 0
	var value []byte
	defer func() { a.trace.log("Getxattr", path, "name=%q len=%d err=%d", name, len(value), errc) }()
	if !a.pathExists(path) {
		errc = -fuse.ENOENT
		return errc, nil
	}
	if name == "com.apple.ResourceFork" {
		errc = -fuse.ENOTSUP
		return errc, nil
	}
	a.mu.Lock()
	if byName := a.xattrs[path]; byName != nil {
		if stored, ok := byName[name]; ok {
			value = append([]byte(nil), stored...)
			a.mu.Unlock()
			return 0, value
		}
	}
	a.mu.Unlock()
	if name == "com.apple.FinderInfo" {
		value = make([]byte, 32)
		return 0, value
	}
	errc = -fuse.ENOATTR
	return errc, nil
}

func (a *adapter) Removexattr(path string, name string) int {
	errc := 0
	defer func() { a.trace.log("Removexattr", path, "name=%q err=%d", name, errc) }()
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if !a.pathExists(path) {
		errc = -fuse.ENOENT
		return errc
	}
	if name == "com.apple.ResourceFork" {
		errc = -fuse.ENOTSUP
		return errc
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if byName := a.xattrs[path]; byName != nil {
		if _, ok := byName[name]; ok {
			delete(byName, name)
			if len(byName) == 0 {
				delete(a.xattrs, path)
			}
			return 0
		}
	}
	if name == "com.apple.FinderInfo" {
		return 0
	}
	errc = -fuse.ENOATTR
	return errc
}

func (a *adapter) Listxattr(path string, fill func(name string) bool) int {
	errc := 0
	defer func() { a.trace.log("Listxattr", path, "err=%d", errc) }()
	if !a.pathExists(path) {
		errc = -fuse.ENOENT
		return errc
	}
	names := []string{"com.apple.FinderInfo"}
	a.mu.Lock()
	if byName := a.xattrs[path]; byName != nil {
		for name := range byName {
			if name != "com.apple.FinderInfo" {
				names = append(names, name)
			}
		}
	}
	a.mu.Unlock()
	for _, name := range names {
		if !fill(name) {
			errc = -fuse.ERANGE
			return errc
		}
	}
	return 0
}

func fillStat(stat *fuse.Stat_t, entry drive.Entry) {
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
	stat.Ino = stableInode(entry)
	stat.Blksize = 4096
	if entry.ModTime.IsZero() {
		entry.ModTime = time.Now()
	}
	stat.Atim = fuse.NewTimespec(entry.ModTime)
	stat.Mtim = stat.Atim
	stat.Ctim = stat.Atim
	stat.Birthtim = stat.Atim
}

func (a *adapter) Setxattr(path string, name string, value []byte, flags int) int {
	errc := 0
	defer func() { a.trace.log("Setxattr", path, "name=%q len=%d flags=%d err=%d", name, len(value), flags, errc) }()
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if !a.pathExists(path) {
		errc = -fuse.ENOENT
		return errc
	}
	if name == "com.apple.ResourceFork" {
		errc = -fuse.ENOTSUP
		return errc
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	byName := a.xattrs[path]
	_, exists := byName[name]
	if flags == fuse.XATTR_CREATE && exists {
		errc = -fuse.EEXIST
		return errc
	}
	if flags == fuse.XATTR_REPLACE && !exists && name != "com.apple.FinderInfo" {
		errc = -fuse.ENOATTR
		return errc
	}
	if byName == nil {
		byName = map[string][]byte{}
		a.xattrs[path] = byName
	}
	byName[name] = append([]byte(nil), value...)
	return 0
}

func (a *adapter) pathExists(path string) bool {
	if a.fs == nil {
		return true
	}
	_, err := a.fs.Stat(context.Background(), path)
	return err == nil
}

func (a *adapter) isReadOnlyPath(path string) bool {
	checker, ok := a.fs.(readOnlyPathChecker)
	return ok && checker.IsReadOnlyPath(path)
}

func childPath(parent, name string) string {
	if parent == "/" {
		return "/" + name
	}
	return strings.TrimRight(parent, "/") + "/" + name
}

func fuseErr(err error) int {
	if errors.Is(err, vfs.ErrReadOnly) {
		return -fuse.EROFS
	}
	return -fuse.EIO
}

func (a *adapter) removeXattrs(path string) {
	a.mu.Lock()
	delete(a.xattrs, path)
	a.mu.Unlock()
}

func (a *adapter) renameXattrs(oldPath, newPath string) {
	a.mu.Lock()
	if byName := a.xattrs[oldPath]; byName != nil {
		a.xattrs[newPath] = byName
		delete(a.xattrs, oldPath)
	}
	a.mu.Unlock()
}

func stableInode(entry drive.Entry) uint64 {
	key := entry.ID
	if key == "" {
		key = entry.ParentID + "/" + entry.Name
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	ino := h.Sum64()
	if ino == 0 {
		return 1
	}
	return ino
}

func unmountCommand(mountPoint string) *exec.Cmd {
	return exec.Command("umount", "-f", mountPoint)
}

type traceLogger struct {
	enabled bool
	logger  *log.Logger
	file    *os.File
}

func newTraceLogger(opts TraceOptions) *traceLogger {
	enabled := opts.Enabled
	path := opts.File
	if envPath := os.Getenv("QRYPT_FUSE_TRACE_FILE"); envPath != "" {
		path = envPath
		enabled = true
	}
	if os.Getenv("QRYPT_FUSE_TRACE") != "" {
		enabled = true
	}
	if !enabled {
		return &traceLogger{}
	}
	out := os.Stderr
	var file *os.File
	if path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			out = f
			file = f
		} else {
			fmt.Fprintf(os.Stderr, "qrypt: open fuse trace file %s: %v\n", path, err)
		}
	}
	return &traceLogger{
		enabled: true,
		logger:  log.New(out, "[fuse] ", log.LstdFlags|log.Lmicroseconds),
		file:    file,
	}
}

func (t *traceLogger) log(op, path, format string, args ...any) {
	if t == nil || !t.enabled || t.logger == nil {
		return
	}
	t.logger.Printf("%s path=%q %s", op, path, fmt.Sprintf(format, args...))
}

func (t *traceLogger) close() {
	if t == nil || t.file == nil {
		return
	}
	t.enabled = false
	_ = t.file.Close()
	t.file = nil
}
