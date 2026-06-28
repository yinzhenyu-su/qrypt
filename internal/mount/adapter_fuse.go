package mount

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/winfsp/cgofuse/fuse"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (a *adapter) Getattr(path string, stat *fuse.Stat_t, fh uint64) int {
	start := time.Now()
	errc := 0
	defer func() { logFuseResult("Getattr", path, start, &errc) }()
	if a.shouldIgnoreAppleMetadata(path) {
		fillStat(stat, a.ignoredAppleEntry(path), path)
		return 0
	}
	entry, err := a.fs.Stat(context.Background(), path)
	if err != nil {
		errc = fuseErr(err)
		if errc == -fuse.ENOENT && fh != 0 {
			if handleEntry, ok := a.handleEntry(fh); ok {
				fillStat(stat, handleEntry, path)
				errc = 0
				return 0
			}
		}
		logFuseError("Getattr", path, errc, err)
		return errc
	}
	fillStat(stat, entry, path)
	if a.isReadOnlyPath(path) {
		stat.Mode &^= 0o222
	}
	logFuseAttrResult(path, stat, entry)
	return 0
}

func (a *adapter) Access(path string, mask uint32) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Access", path, "mask=%d err=%d dur=%s", mask, errc, time.Since(start)) }()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, true)
		return 0
	}
	if mask&fuse.W_OK != 0 && a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if _, err := a.fs.Stat(context.Background(), path); err != nil {
		errc = fuseErr(err)
		logFuseError("Access", path, errc, err)
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
	if a.shouldIgnoreAppleMetadata(path) {
		return 0
	}
	entries, err := a.fs.List(context.Background(), path)
	if err != nil {
		errc = fuseErr(err)
		logFuseError("Readdir", path, errc, err)
		return errc
	}
	for _, entry := range entries {
		st := &fuse.Stat_t{}
		fillStat(st, entry, childPath(path, entry.Name))
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

func (a *adapter) Opendir(path string) (int, uint64) {
	start := time.Now()
	errc := 0
	var fh uint64
	defer func() { a.trace.log("Opendir", path, "fh=%d err=%d dur=%s", fh, errc, time.Since(start)) }()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, true)
		fh = a.nextHandle(path, a.ignoredAppleEntry(path))
		return 0, fh
	}
	entry, err := a.fs.Stat(context.Background(), path)
	if err != nil {
		errc = fuseErr(err)
		logFuseError("Opendir", path, errc, err)
		return errc, 0
	}
	if !entry.IsDir {
		errc = -fuse.ENOTDIR
		return errc, 0
	}
	fh = a.nextHandle(path, entry)
	return 0, fh
}

func (a *adapter) Releasedir(path string, fh uint64) int {
	start := time.Now()
	defer func() { a.trace.log("Releasedir", path, "fh=%d dur=%s", fh, time.Since(start)) }()
	a.releaseHandle(fh)
	return 0
}

func (a *adapter) Fsyncdir(path string, datasync bool, fh uint64) int {
	errc := 0
	defer func() { a.trace.log("Fsyncdir", path, "datasync=%t fh=%d err=%d", datasync, fh, errc) }()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, true)
		return 0
	}
	if _, err := a.fs.Stat(context.Background(), path); err != nil {
		errc = fuseErr(err)
		logFuseError("Fsyncdir", path, errc, err)
	}
	return errc
}

func (a *adapter) Open(path string, flags int) (int, uint64) {
	start := time.Now()
	errc := 0
	var fh uint64
	defer func() { a.trace.log("Open", path, "flags=%d fh=%d err=%d dur=%s", flags, fh, errc, time.Since(start)) }()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		fh = a.nextHandle(path, a.ignoredAppleEntry(path))
		return 0, fh
	}
	entry, err := a.fs.Stat(context.Background(), path)
	if err != nil {
		errc = fuseErr(err)
		logFuseError("Open", path, errc, err)
		return errc, 0
	}
	fh = a.nextHandle(path, entry)
	return 0, fh
}

func (a *adapter) Mknod(path string, mode uint32, dev uint64) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Mknod", path, "mode=%o dev=%d err=%d dur=%s", mode, dev, errc, time.Since(start)) }()
	if a.isStopping() {
		errc = -fuse.EIO
		return errc
	}
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, mode&fuse.S_IFDIR != 0)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if mode&fuse.S_IFDIR != 0 {
		errc = a.Mkdir(path, mode)
		return errc
	}
	if mode&fuse.S_IFREG == 0 && mode&fuse.S_IFMT != 0 {
		errc = -fuse.ENOSYS
		return errc
	}
	if isFinderDirectoryCreate(path) {
		errc = a.Mkdir(path, mode)
		return errc
	}
	if err := a.fs.Create(context.Background(), path); err != nil {
		errc = fuseErr(err)
		logFuseError("Mknod", path, errc, err)
		return errc
	}
	return 0
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
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		fh = a.nextHandle(path, a.ignoredAppleEntry(path))
		return 0, fh
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc, 0
	}
	if isFinderDirectoryCreate(path) {
		errc = a.Mkdir(path, mode)
		return errc, 0
	}
	if err := a.fs.Create(context.Background(), path); err != nil {
		errc = fuseErr(err)
		logFuseError("Create", path, errc, err)
		return errc, 0
	}
	if entry, err := a.fs.Stat(context.Background(), path); err == nil {
		fh = a.nextHandle(path, entry)
	} else {
		fh = a.nextHandle(path)
	}
	return 0, fh
}

func (a *adapter) Read(path string, buff []byte, ofst int64, fh uint64) int {
	start := time.Now()
	result := 0
	defer func() {
		a.trace.log("Read", path, "fh=%d ofst=%d len=%d result=%d dur=%s", fh, ofst, len(buff), result, time.Since(start))
	}()
	if a.shouldIgnoreAppleMetadata(path) {
		result = a.readIgnoredApple(path, buff, ofst)
		return result
	}
	rc, err := a.fs.Read(context.Background(), path, ofst, int64(len(buff)))
	if err != nil {
		result = -fuse.EIO
		logFuseError("Read", path, result, err)
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
		logFuseError("Read", path, result, err)
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
	if a.shouldIgnoreAppleMetadata(path) {
		a.writeIgnoredApple(path, int64(len(buff)), ofst)
		result = len(buff)
		return result
	}
	if a.isReadOnlyPath(path) {
		result = -fuse.EROFS
		return result
	}
	n, err := a.fs.WriteAt(context.Background(), path, buff, ofst)
	if err != nil {
		result = fuseErr(err)
		logFuseError("Write", path, result, err)
		return result
	}
	result = n
	return n
}

func (a *adapter) Flush(path string, fh uint64) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Flush", path, "fh=%d err=%d dur=%s", fh, errc, time.Since(start)) }()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		return 0
	}
	if err := a.fs.Flush(context.Background(), path); err != nil {
		errc = fuseErr(err)
		logFuseError("Flush", path, errc, err)
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
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, true)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if _, err := a.fs.Mkdir(context.Background(), path); err != nil {
		errc = fuseErr(err)
		logFuseError("Mkdir", path, errc, err)
		return errc
	}
	return 0
}

func (a *adapter) Unlink(path string) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Unlink", path, "err=%d dur=%s", errc, time.Since(start)) }()
	if a.shouldIgnoreAppleMetadata(path) {
		a.removeIgnoredApple(path)
		a.removeXattrs(path)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if err := a.fs.Remove(context.Background(), path); err != nil {
		errc = fuseErr(err)
		logFuseError("Unlink", path, errc, err)
		return errc
	}
	a.removeXattrs(path)
	return 0
}

func (a *adapter) Rmdir(path string) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Rmdir", path, "err=%d dur=%s", errc, time.Since(start)) }()
	if a.shouldIgnoreAppleMetadata(path) {
		a.removeIgnoredApple(path)
		a.removeXattrs(path)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if err := a.fs.RemoveDir(context.Background(), path); err != nil {
		errc = fuseErr(err)
		logFuseError("Rmdir", path, errc, err)
		return errc
	}
	a.removeXattrs(path)
	return 0
}

func (a *adapter) Rename(oldPath, newPath string) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Rename", oldPath, "new=%q err=%d dur=%s", newPath, errc, time.Since(start)) }()
	if a.shouldIgnoreAppleMetadata(oldPath) || a.shouldIgnoreAppleMetadata(newPath) {
		a.renameIgnoredApple(oldPath, newPath)
		a.renameXattrs(oldPath, newPath)
		return 0
	}
	if a.isReadOnlyPath(oldPath) || a.isReadOnlyPath(newPath) {
		errc = -fuse.EROFS
		return errc
	}
	if err := a.fs.Rename(context.Background(), oldPath, newPath); err != nil {
		errc = fuseErr(err)
		logFuseError("Rename", oldPath, errc, err)
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
	if a.shouldIgnoreAppleMetadata(path) {
		a.truncateIgnoredApple(path, size)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if err := a.fs.Truncate(context.Background(), path, size); err != nil {
		errc = fuseErr(err)
		logFuseError("Truncate", path, errc, err)
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
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		errc = 0
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
func (a *adapter) Chmod(path string, mode uint32) int {
	errc := 0
	defer func() { a.trace.log("Chmod", path, "mode=%o err=%d", mode, errc) }()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	return 0
}

func (a *adapter) Chown(path string, uid uint32, gid uint32) int {
	errc := 0
	defer func() { a.trace.log("Chown", path, "uid=%d gid=%d err=%d", uid, gid, errc) }()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	return 0
}

func (a *adapter) Chflags(path string, flags uint32) int {
	errc := 0
	defer func() { a.trace.log("Chflags", path, "flags=%d err=%d", flags, errc) }()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if _, err := a.fs.Stat(context.Background(), path); err != nil {
		errc = fuseErr(err)
		logFuseError("Chflags", path, errc, err)
		return errc
	}
	return 0
}

func (a *adapter) Setcrtime(path string, tmsp fuse.Timespec) int {
	errc := 0
	defer func() { a.trace.log("Setcrtime", path, "sec=%d nsec=%d err=%d", tmsp.Sec, tmsp.Nsec, errc) }()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if _, err := a.fs.Stat(context.Background(), path); err != nil {
		errc = fuseErr(err)
		logFuseError("Setcrtime", path, errc, err)
		return errc
	}
	return 0
}

func (a *adapter) Setchgtime(path string, tmsp fuse.Timespec) int {
	errc := 0
	defer func() { a.trace.log("Setchgtime", path, "sec=%d nsec=%d err=%d", tmsp.Sec, tmsp.Nsec, errc) }()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if _, err := a.fs.Stat(context.Background(), path); err != nil {
		errc = fuseErr(err)
		logFuseError("Setchgtime", path, errc, err)
		return errc
	}
	return 0
}

func (a *adapter) Getxattr(path string, name string) (int, []byte) {
	errc := -fuse.ENOATTR
	defer func() { a.trace.log("Getxattr", path, "name=%q len=%d err=%d", name, 0, errc) }()
	if a.shouldIgnoreAppleXattr(name) {
		return errc, nil
	}
	return errc, nil
}

func (a *adapter) Removexattr(path string, name string) int {
	errc := 0
	defer func() { a.trace.log("Removexattr", path, "name=%q err=%d", name, errc) }()
	if a.shouldIgnoreAppleXattr(name) {
		return 0
	}
	return 0
}

func (a *adapter) Listxattr(path string, fill func(name string) bool) int {
	errc := 0
	defer func() { a.trace.log("Listxattr", path, "err=%d", errc) }()
	return 0
}
func (a *adapter) Setxattr(path string, name string, value []byte, flags int) int {
	errc := 0
	defer func() { a.trace.log("Setxattr", path, "name=%q len=%d flags=%d err=%d", name, len(value), flags, errc) }()
	if name == "com.apple.finder.copy.source" {
		if preparer, ok := a.fs.(directoryCopyPreparer); ok {
			if err := preparer.PrepareDirectoryCopy(context.Background(), path); err != nil {
				errc = fuseErr(err)
				logFuseError("PrepareDirectoryCopy", path, errc, err)
				return errc
			}
			a.trace.log("PrepareDirectoryCopy", path, "xattr=%q err=0", name)
		}
	}
	if a.shouldIgnoreAppleXattr(name) {
		return 0
	}
	return 0
}
func (a *adapter) shouldIgnoreAppleMetadata(path string) bool {
	return a.ignoreAppleMetadata && (isAppleMetadataPath(path) || a.hasIgnoredApple(path))
}

func (a *adapter) shouldIgnoreAppleXattr(name string) bool {
	return a.ignoreAppleXattr && strings.HasPrefix(strings.ToLower(name), "com.apple.")
}

func (a *adapter) hasIgnoredApple(path string) bool {
	key := cleanMountPath(path)
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.ignoredApple[key]; ok {
		return true
	}
	for existing, node := range a.ignoredApple {
		if node.isDir && strings.HasPrefix(key, existing+"/") {
			return true
		}
	}
	return false
}

func (a *adapter) ensureIgnoredApple(path string, isDir bool) ignoredAppleNode {
	key := cleanMountPath(path)
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	node, ok := a.ignoredApple[key]
	if !ok {
		node = ignoredAppleNode{isDir: isDir, mtime: now}
	} else {
		node.isDir = node.isDir || isDir
		node.mtime = now
	}
	a.ignoredApple[key] = node
	return node
}

func (a *adapter) ignoredAppleEntry(path string) drive.Entry {
	key := cleanMountPath(path)
	a.mu.Lock()
	node, ok := a.ignoredApple[key]
	a.mu.Unlock()
	if !ok {
		node = ignoredAppleNode{isDir: isAppleMetadataDirPath(path), mtime: time.Now()}
	}
	return drive.Entry{
		ID:      "ignored-apple-metadata:" + key,
		Name:    filepath.Base(key),
		Size:    node.size,
		IsDir:   node.isDir,
		ModTime: node.mtime,
	}
}

func (a *adapter) writeIgnoredApple(path string, length, off int64) {
	if off < 0 {
		off = 0
	}
	key := cleanMountPath(path)
	now := time.Now()
	a.mu.Lock()
	node := a.ignoredApple[key]
	node.isDir = false
	if end := off + length; end > node.size {
		node.size = end
	}
	node.mtime = now
	a.ignoredApple[key] = node
	a.mu.Unlock()
}

func (a *adapter) readIgnoredApple(path string, buff []byte, off int64) int {
	if off < 0 {
		return 0
	}
	key := cleanMountPath(path)
	a.mu.Lock()
	node := a.ignoredApple[key]
	a.mu.Unlock()
	if node.isDir || off >= node.size {
		return 0
	}
	remaining := node.size - off
	if remaining > int64(len(buff)) {
		remaining = int64(len(buff))
	}
	n := int(remaining)
	clear(buff[:n])
	return n
}

func (a *adapter) truncateIgnoredApple(path string, size int64) {
	if size < 0 {
		size = 0
	}
	key := cleanMountPath(path)
	now := time.Now()
	a.mu.Lock()
	node := a.ignoredApple[key]
	node.isDir = false
	node.size = size
	node.mtime = now
	a.ignoredApple[key] = node
	a.mu.Unlock()
}

func (a *adapter) removeIgnoredApple(path string) {
	key := cleanMountPath(path)
	a.mu.Lock()
	for existing := range a.ignoredApple {
		if existing == key || strings.HasPrefix(existing, key+"/") {
			delete(a.ignoredApple, existing)
		}
	}
	a.mu.Unlock()
}

func (a *adapter) renameIgnoredApple(oldPath, newPath string) {
	oldKey := cleanMountPath(oldPath)
	newKey := cleanMountPath(newPath)
	now := time.Now()
	a.mu.Lock()
	for existing, node := range a.ignoredApple {
		if existing != oldKey && !strings.HasPrefix(existing, oldKey+"/") {
			continue
		}
		next := newKey + strings.TrimPrefix(existing, oldKey)
		delete(a.ignoredApple, existing)
		node.mtime = now
		a.ignoredApple[next] = node
	}
	if _, ok := a.ignoredApple[newKey]; !ok {
		a.ignoredApple[newKey] = ignoredAppleNode{isDir: isAppleMetadataDirPath(newPath), mtime: now}
	}
	a.mu.Unlock()
}

func childPath(parent, name string) string {
	if parent == "/" {
		return "/" + name
	}
	return strings.TrimRight(parent, "/") + "/" + name
}

func isFinderDirectoryCreate(path string) bool {
	name := filepath.Base(path)
	return !strings.Contains(name, ".") && !strings.HasPrefix(name, ".")
}

func isAppleMetadataPath(path string) bool {
	segments := strings.Split(cleanMountPath(path), "/")
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		if isAppleMetadataName(segment) || isAppleMetadataDirName(segment) {
			return true
		}
	}
	return false
}

func isAppleMetadataDirPath(path string) bool {
	for _, segment := range strings.Split(cleanMountPath(path), "/") {
		if isAppleMetadataDirName(segment) {
			return true
		}
	}
	return false
}

func isAppleMetadataName(name string) bool {
	return name == ".DS_Store" ||
		name == ".VolumeIcon.icns" ||
		name == ".metadata_never_index" ||
		name == ".com.apple.timemachine.donotpresent" ||
		strings.HasPrefix(name, "._")
}

func isAppleMetadataDirName(name string) bool {
	switch name {
	case ".Spotlight-V100", ".Trashes", ".fseventsd", ".TemporaryItems", ".DocumentRevisions-V100", "__MACOSX":
		return true
	default:
		return false
	}
}

func cleanMountPath(path string) string {
	return filepath.ToSlash(filepath.Clean("/" + strings.TrimPrefix(path, "/")))
}
