package mount

import (
	"time"

	"github.com/winfsp/cgofuse/fuse"
)

func (a *adapter) Getattr(path string, stat *fuse.Stat_t, fh uint64) int {
	start := time.Now()
	errc := 0
	defer func() { logFuseResult("Getattr", path, start, &errc) }()
	ctx, done, ok := a.beginOp("Getattr", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		fillStat(stat, a.ignoredAppleEntry(path), path)
		return 0
	}
	entry, err := a.fs.Stat(ctx, path)
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
	ctx, done, ok := a.beginOp("Access", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, true)
		return 0
	}
	if mask&fuse.W_OK != 0 && a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if _, err := a.fs.Stat(ctx, path); err != nil {
		errc = fuseErr(err)
		logFuseError("Access", path, errc, err)
		return errc
	}
	return 0
}
