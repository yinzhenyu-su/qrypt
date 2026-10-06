//go:build !fuse2

package mount

// FUSE3 removed the -o use_ino mount option; inode numbers from the
// filesystem are always used, so nothing needs to be passed (see mount.go).
const fuseUseInoOption = ""
