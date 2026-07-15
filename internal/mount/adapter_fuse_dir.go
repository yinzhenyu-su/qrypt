package mount

import (
	"time"

	"github.com/winfsp/cgofuse/fuse"
)

func (a *adapter) Readdir(path string, fill func(name string, stat *fuse.Stat_t, ofst int64) bool, ofst int64, fh uint64) int {
	start := time.Now()
	errc := 0
	count := 0
	defer func() {
		a.trace.log("Readdir", path, "fh=%d ofst=%d count=%d err=%d dur=%s", fh, ofst, count, errc, time.Since(start))
	}()
	ctx, done, ok := a.beginOp("Readdir", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	fill(".", nil, 0)
	fill("..", nil, 0)
	if a.hasIgnoredAppleMetadata(path) {
		return 0
	}
	entries, err := a.fs.List(ctx, path)
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
	ctx, done, ok := a.beginOp("Opendir", path)
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
	ctx, done, ok := a.beginOp("Fsyncdir", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if a.hasIgnoredAppleMetadata(path) {
		return 0
	}
	if _, err := a.fs.Stat(ctx, path); err != nil {
		errc = fuseErr(err)
		logFuseError("Fsyncdir", path, errc, err)
	}
	return errc
}

func (a *adapter) Mkdir(path string, mode uint32) int {
	start := time.Now()
	errc := 0
	defer func() { a.trace.log("Mkdir", path, "mode=%o err=%d dur=%s", mode, errc, time.Since(start)) }()
	ctx, done, ok := a.beginOp("Mkdir", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, true)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if _, err := a.fs.Mkdir(ctx, path); err != nil {
		errc = fuseErr(err)
		logFuseError("Mkdir", path, errc, err)
		return errc
	}
	return 0
}
