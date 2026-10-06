//go:build fuse2

package mount

// FUSE2 accepts -o use_ino so the kernel keeps the inode numbers handed to
// it by the filesystem (see mount.go).
const fuseUseInoOption = "use_ino"
