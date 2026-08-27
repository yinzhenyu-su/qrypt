//go:build staticfuse && fuse3

package mount

// fuse3 removed the -o use_ino mount option; inode numbers from the
// filesystem are always used, so nothing needs to be passed (see mount.go).
const fuseUseInoOption = ""
