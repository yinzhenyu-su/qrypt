package mount

import "github.com/winfsp/cgofuse/fuse"

func (a *adapter) Getxattr(path string, name string) (int, []byte) {
	errc := -fuse.ENOATTR
	valueLen := 0
	defer func() { a.trace.log("Getxattr", path, "name=%q len=%d err=%d", name, valueLen, errc) }()
	if a.shouldIgnoreAppleXattr(name) {
		return errc, nil
	}
	value, ok := a.getXattr(path, name)
	if !ok {
		return errc, nil
	}
	errc = len(value)
	valueLen = len(value)
	return errc, value
}

func (a *adapter) Removexattr(path string, name string) int {
	errc := 0
	defer func() { a.trace.log("Removexattr", path, "name=%q err=%d", name, errc) }()
	if a.shouldIgnoreAppleXattr(name) {
		return 0
	}
	a.removeXattr(path, name)
	return 0
}

func (a *adapter) Listxattr(path string, fill func(name string) bool) int {
	errc := 0
	defer func() { a.trace.log("Listxattr", path, "err=%d", errc) }()
	for _, name := range a.listXattrs(path) {
		if a.shouldIgnoreAppleXattr(name) {
			continue
		}
		if !fill(name) {
			break
		}
	}
	return 0
}

func (a *adapter) Setxattr(path string, name string, value []byte, flags int) int {
	errc := 0
	defer func() { a.trace.log("Setxattr", path, "name=%q len=%d flags=%d err=%d", name, len(value), flags, errc) }()
	ctx, done, ok := a.beginOp("Setxattr", path)
	if !ok {
		errc = -fuse.EIO
		return errc
	}
	defer done()
	if name == "com.apple.finder.copy.source" {
		if preparer, ok := a.fs.(directoryCopyPreparer); ok {
			if err := preparer.PrepareDirectoryCopy(ctx, path); err != nil {
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
	a.setXattr(path, name, value)
	return errc
}
