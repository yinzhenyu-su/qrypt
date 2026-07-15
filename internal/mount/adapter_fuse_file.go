package mount

import (
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/winfsp/cgofuse/fuse"
)

func (a *adapter) Open(path string, flags int) (int, uint64) {
	start := time.Now()
	errc := 0
	var fh uint64
	defer func() { a.trace.log("Open", path, "flags=%d fh=%d err=%d dur=%s", flags, fh, errc, time.Since(start)) }()
	ctx, done, ok := a.beginOp("Open", path)
	if !ok {
		errc = -fuse.EIO
		return errc, 0
	}
	defer done()
	if a.hasIgnoredAppleMetadata(path) {
		fh = a.nextHandle(path, a.ignoredAppleEntry(path))
		return 0, fh
	}
	entry, err := a.fs.Stat(ctx, path)
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
	ctx, done, ok := a.beginOp("Mknod", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		if !isAppleMetadataDirPath(path) {
			if err := a.ensureIgnoredAppleParent(ctx, path); err != nil {
				errc = fuseErr(err)
				logFuseError("Mknod", path, errc, err)
				return errc
			}
		}
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
	if err := a.fs.Create(ctx, path); err != nil {
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
	ctx, done, ok := a.beginOp("Create", path)
	if !ok {
		errc = -fuse.EIO
		return errc, 0
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		if !isAppleMetadataDirPath(path) {
			if err := a.ensureIgnoredAppleParent(ctx, path); err != nil {
				errc = fuseErr(err)
				logFuseError("Create", path, errc, err)
				return errc, 0
			}
		}
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
	if err := a.fs.Create(ctx, path); err != nil {
		errc = fuseErr(err)
		logFuseError("Create", path, errc, err)
		return errc, 0
	}
	if entry, err := a.fs.Stat(ctx, path); err == nil {
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
	ctx, done, ok := a.beginOp("Read", path)
	if !ok {
		result = -fuse.EIO
		return result
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		result = a.readIgnoredApple(path, buff, ofst)
		return result
	}
	rc, err := a.fs.Read(ctx, path, ofst, int64(len(buff)))
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
	ctx, done, ok := a.beginOp("Write", path)
	if !ok {
		result = -fuse.EIO
		return result
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		if !isAppleMetadataDirPath(path) {
			if err := a.ensureIgnoredAppleParent(ctx, path); err != nil {
				result = fuseErr(err)
				logFuseError("Write", path, result, err)
				return result
			}
		}
		a.writeIgnoredApple(path, buff, ofst)
		result = len(buff)
		return result
	}
	if a.isReadOnlyPath(path) {
		result = -fuse.EROFS
		return result
	}
	n, err := a.fs.WriteAt(ctx, path, buff, ofst)
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
	ctx, done, ok := a.beginOp("Flush", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		return 0
	}
	if err := a.fs.Flush(ctx, path); err != nil {
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

func (a *adapter) Truncate(path string, size int64, fh uint64) int {
	start := time.Now()
	errc := 0
	defer func() {
		a.trace.log("Truncate", path, "fh=%d size=%d err=%d dur=%s", fh, size, errc, time.Since(start))
	}()
	ctx, done, ok := a.beginOp("Truncate", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		if !isAppleMetadataDirPath(path) {
			if err := a.ensureIgnoredAppleParent(ctx, path); err != nil {
				errc = fuseErr(err)
				logFuseError("Truncate", path, errc, err)
				return errc
			}
		}
		a.truncateIgnoredApple(path, size)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if err := a.fs.Truncate(ctx, path, size); err != nil {
		errc = fuseErr(err)
		logFuseError("Truncate", path, errc, err)
		return errc
	}
	return 0
}

func (a *adapter) Ftruncate(path string, size int64, fh uint64) int {
	return a.Truncate(path, size, fh)
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
