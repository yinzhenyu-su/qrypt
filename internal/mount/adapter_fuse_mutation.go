package mount

import (
	"time"

	"github.com/winfsp/cgofuse/fuse"
)

func (a *adapter) Unlink(path string) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Unlink", path, "err=%d dur=%s", errc, time.Since(start)) }()
	ctx, done, ok := a.beginOp("Unlink", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		a.removeIgnoredApple(path)
		a.removeXattrs(path)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if err := a.fs.Remove(ctx, path); err != nil {
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
	ctx, done, ok := a.beginOp("Rmdir", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		a.removeIgnoredApple(path)
		a.removeXattrs(path)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if err := a.fs.RemoveDir(ctx, path); err != nil {
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
	ctx, done, ok := a.beginOp("Rename", oldPath)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(oldPath) || a.shouldIgnoreAppleMetadata(newPath) {
		a.renameIgnoredApple(oldPath, newPath)
		a.renameXattrs(oldPath, newPath)
		return 0
	}
	if a.isReadOnlyPath(oldPath) || a.isReadOnlyPath(newPath) {
		errc = -fuse.EROFS
		return errc
	}
	if err := a.fs.Rename(ctx, oldPath, newPath); err != nil {
		errc = fuseErr(err)
		logFuseError("Rename", oldPath, errc, err)
		return errc
	}
	a.renameXattrs(oldPath, newPath)
	return 0
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
