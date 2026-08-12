package mount

import (
	"time"

	"github.com/winfsp/cgofuse/fuse"
)

func (a *adapter) Utimens(path string, tmsp []fuse.Timespec) int {
	errc := 0
	ctx, done, ok := a.beginOp("Utimens", path)
	if !ok {
		errc = -fuse.EIO
		a.trace.log("Utimens", path, "count=%d err=%d", len(tmsp), errc)
		return errc
	}
	defer done()
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
	}
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		a.trace.log("Utimens", path, "count=%d err=%d", len(tmsp), 0)
		return 0
	}
	if errc == 0 && len(tmsp) >= 2 {
		if setter, ok := a.fs.(modTimeSetter); ok {
			modTime := time.Unix(tmsp[1].Sec, tmsp[1].Nsec)
			if err := setter.SetModTime(ctx, path, modTime); err != nil {
				errc = fuseErr(err)
				logFuseError("Utimens", path, errc, err)
			}
		}
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
	ctx, done, ok := a.beginOp("Chflags", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if _, err := a.fs.Stat(ctx, path); err != nil {
		errc = fuseErr(err)
		logFuseError("Chflags", path, errc, err)
		return errc
	}
	return 0
}

func (a *adapter) Setcrtime(path string, tmsp fuse.Timespec) int {
	errc := 0
	defer func() { a.trace.log("Setcrtime", path, "sec=%d nsec=%d err=%d", tmsp.Sec, tmsp.Nsec, errc) }()
	ctx, done, ok := a.beginOp("Setcrtime", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if _, err := a.fs.Stat(ctx, path); err != nil {
		errc = fuseErr(err)
		logFuseError("Setcrtime", path, errc, err)
		return errc
	}
	return 0
}

func (a *adapter) Setchgtime(path string, tmsp fuse.Timespec) int {
	errc := 0
	defer func() { a.trace.log("Setchgtime", path, "sec=%d nsec=%d err=%d", tmsp.Sec, tmsp.Nsec, errc) }()
	ctx, done, ok := a.beginOp("Setchgtime", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if a.shouldIgnoreAppleMetadata(path) {
		a.ensureIgnoredApple(path, false)
		return 0
	}
	if a.isReadOnlyPath(path) {
		errc = -fuse.EROFS
		return errc
	}
	if _, err := a.fs.Stat(ctx, path); err != nil {
		errc = fuseErr(err)
		logFuseError("Setchgtime", path, errc, err)
		return errc
	}
	return 0
}
