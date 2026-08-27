package mount

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/winfsp/cgofuse/fuse"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type Options struct {
	MountPoint         string
	ReadOnly           bool
	AllowOther         bool
	DefaultPermissions bool
	VolumeName         string
	PlatformOptions    map[string][]string
	AttrTimeout        time.Duration
	AttrTimeoutSet     bool
	EntryTimeout       time.Duration
	EntryTimeoutSet    bool
	NegativeTimeout    time.Duration
	TotalSpace         int64
	FreeSpace          int64
	Foreground         bool
	ReadyTimeout       time.Duration
	UnmountOnError     bool
}

type Session struct {
	ID                       string
	MountPoint               string
	host                     *fuse.FileSystemHost
	adapter                  *adapter
	unsubscribeInvalidations func()
}

type Mounter interface {
	Mount(ctx context.Context, fs vfs.FileSystem, opts Options) (*Session, error)
	Unmount(ctx context.Context, session *Session) error
}

type FuseMounter struct{}

func NewMounter() Mounter {
	return FuseMounter{}
}

func (FuseMounter) Mount(ctx context.Context, fs vfs.FileSystem, opts Options) (*Session, error) {
	if fs == nil {
		return nil, fmt.Errorf("mount: filesystem is nil")
	}
	if opts.MountPoint == "" {
		return nil, fmt.Errorf("mount: mount point required")
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 5 * time.Second
	}
	if !isDriveLetter(opts.MountPoint) {
		if err := os.MkdirAll(opts.MountPoint, 0o755); err != nil {
			return nil, err
		}
	}

	ad := newAdapterWithOptions(fs, adapterOptions{
		Statfs: StatfsOptions{
			TotalSpace: opts.TotalSpace,
			FreeSpace:  opts.FreeSpace,
		},
		ReadOnly:   opts.ReadOnly,
		AllowOther: opts.AllowOther,
	})
	host := fuse.NewFileSystemHost(ad)
	// qrypt's adapter and VFS synchronize their mutable state internally, so
	// macFUSE may safely run reads of the same vnode concurrently. Without this
	// capability, one slow remote read blocks a seek on another file handle.
	host.SetCapNodeRWLock(true)
	session := &Session{
		ID:         opts.MountPoint,
		MountPoint: opts.MountPoint,
		host:       host,
		adapter:    ad,
	}
	session.unsubscribeInvalidations = subscribeInvalidations(fs, host.Notify)

	mountOpts := mountOptions(opts)
	result := make(chan bool, 1)
	go func() {
		result <- host.Mount(opts.MountPoint, mountOpts)
	}()
	readyTimer := time.NewTimer(opts.ReadyTimeout)
	defer readyTimer.Stop()

	select {
	case <-ctx.Done():
		session.unsubscribeInvalidations()
		ad.shutdown()
		host.Unmount()
		return nil, ctx.Err()
	case ok := <-result:
		if !ok {
			session.unsubscribeInvalidations()
			host.Unmount()
			return nil, fmt.Errorf("mount: failed to mount %s", opts.MountPoint)
		}
		return session, nil
	case <-ad.ready:
		return session, nil
	case <-readyTimer.C:
		return session, nil
	}
}

func (FuseMounter) Unmount(ctx context.Context, session *Session) error {
	if session == nil {
		return nil
	}
	start := time.Now()
	logging.L.Infof("[FUSE] unmount start mount=%q", session.MountPoint)
	if session.unsubscribeInvalidations != nil {
		session.unsubscribeInvalidations()
	}
	if session.adapter != nil {
		stepStart := time.Now()
		session.adapter.shutdown()
		logging.L.Infof("[FUSE] adapter shutdown complete mount=%q dur=%s", session.MountPoint, time.Since(stepStart))
	}
	if session.host != nil {
		stepStart := time.Now()
		session.host.Unmount()
		logging.L.Infof("[FUSE] host unmount complete mount=%q dur=%s", session.MountPoint, time.Since(stepStart))
	}
	if cmd := unmountCommand(session.MountPoint); cmd != nil {
		stepStart := time.Now()
		if err := cmd.Run(); err != nil {
			logging.L.Warnf("[FUSE] system unmount returned mount=%q dur=%s err=%v", session.MountPoint, time.Since(stepStart), err)
		} else {
			logging.L.Infof("[FUSE] system unmount complete mount=%q dur=%s", session.MountPoint, time.Since(stepStart))
		}
	}
	logging.L.Infof("[FUSE] unmount complete mount=%q dur=%s", session.MountPoint, time.Since(start))
	return nil
}

func mountOptions(opts Options) []string {
	return mountOptionsForGOOS(opts, runtime.GOOS)
}

func mountOptionsForGOOS(opts Options, goos string) []string {
	mode := "rw"
	if opts.ReadOnly {
		mode = "ro"
	}
	attrTimeout := opts.AttrTimeout
	if attrTimeout == 0 && !opts.AttrTimeoutSet {
		attrTimeout = time.Second
	}
	entryTimeout := opts.EntryTimeout
	if entryTimeout == 0 && !opts.EntryTimeoutSet {
		entryTimeout = time.Second
	}
	flags := []string{
		"-o", mode,
		"-o", "attr_timeout=" + fuseTimeout(attrTimeout),
		"-o", "entry_timeout=" + fuseTimeout(entryTimeout),
		"-o", "negative_timeout=" + fuseTimeout(opts.NegativeTimeout),
	}
	// fuse2 takes -o use_ino so the kernel keeps the inode numbers handed to
	// it by the filesystem; fuse3 removed the option (it always uses them).
	if fuseUseInoOption != "" {
		flags = append(flags, "-o", fuseUseInoOption)
	}
	if goos == "darwin" {
		flags = append(flags,
			"-o", "fsname=qrypt",
			"-o", "subtype=qrypt",
		)
	}
	platformOptions := opts.PlatformOptions[goos]
	if opts.PlatformOptions == nil && goos == "darwin" {
		platformOptions = []string{"defer_permissions", "auto_xattr", "iosize=4194304"}
	}
	for _, option := range platformOptions {
		flags = append(flags, "-o", option)
	}
	if goos == "windows" {
		flags = append(flags,
			"-o", "fsname=qrypt",
		)
	}
	if opts.AllowOther {
		flags = append(flags, "-o", "allow_other")
	}
	if opts.DefaultPermissions {
		flags = append(flags, "-o", "default_permissions")
	}
	// volname is a macFUSE/WinFsp option; Linux libfuse rejects it with
	// "unknown option", which would make mounting fail on Linux. The
	// volume identity on Linux is carried by fsname/subtype above.
	if opts.VolumeName != "" && goos != "linux" {
		flags = append(flags, "-o", "volname="+opts.VolumeName)
	}
	return flags
}

// isDriveLetter returns true when the mount point is a Windows drive letter
// (e.g. "X:" or "X:\\"). In that case MkdirAll must be skipped because
// drive letters cannot be created as directories.
func isDriveLetter(mountPoint string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	if len(mountPoint) < 2 || len(mountPoint) > 3 {
		return false
	}
	c := mountPoint[0]
	if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
		return false
	}
	if mountPoint[1] != ':' {
		return false
	}
	if len(mountPoint) == 3 && mountPoint[2] != '\\' && mountPoint[2] != '/' {
		return false
	}
	return true
}

func fuseTimeout(d time.Duration) string {
	if d <= 0 {
		return "0"
	}
	if d%time.Second == 0 {
		return fmt.Sprintf("%d", int64(d/time.Second))
	}
	return fmt.Sprintf("%.3f", d.Seconds())
}
