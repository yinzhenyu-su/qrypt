//go:build !(staticfuse && fuse3)

package mount

// fuse2 accepts -o use_ino so the kernel keeps the inode numbers handed to
// it by the filesystem (see mount.go).
const fuseUseInoOption = "use_ino"
