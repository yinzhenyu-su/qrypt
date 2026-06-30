package mount

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/winfsp/cgofuse/fuse"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type Options struct {
	MountPoint         string
	ReadOnly           bool
	AllowOther         bool
	DefaultPermissions bool
	VolumeName         string
	NoAppleDouble      bool
	NoAppleXattr       bool
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
	ID         string
	MountPoint string
	host       *fuse.FileSystemHost
	adapter    *adapter
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
	if err := os.MkdirAll(opts.MountPoint, 0o755); err != nil {
		return nil, err
	}

	ad := newAdapterWithOptions(fs, adapterOptions{
		Statfs: StatfsOptions{
			TotalSpace: opts.TotalSpace,
			FreeSpace:  opts.FreeSpace,
		},
		ReadOnly:            opts.ReadOnly,
		IgnoreAppleMetadata: opts.NoAppleDouble,
		IgnoreAppleXattr:    opts.NoAppleXattr,
	})
	host := fuse.NewFileSystemHost(ad)
	session := &Session{
		ID:         opts.MountPoint,
		MountPoint: opts.MountPoint,
		host:       host,
		adapter:    ad,
	}

	mountOpts := mountOptions(opts)
	result := make(chan bool, 1)
	go func() {
		result <- host.Mount(opts.MountPoint, mountOpts)
	}()

	select {
	case <-ctx.Done():
		ad.shutdown()
		host.Unmount()
		return nil, ctx.Err()
	case ok := <-result:
		if !ok {
			host.Unmount()
			return nil, fmt.Errorf("mount: failed to mount %s", opts.MountPoint)
		}
		return session, nil
	case <-time.After(opts.ReadyTimeout):
		return session, nil
	}
}

func (FuseMounter) Unmount(ctx context.Context, session *Session) error {
	if session == nil {
		return nil
	}
	if session.adapter != nil {
		session.adapter.shutdown()
	}
	if session.host != nil {
		session.host.Unmount()
	}
	if cmd := unmountCommand(session.MountPoint); cmd != nil {
		_ = cmd.Run()
	}
	return nil
}

func mountOptions(opts Options) []string {
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
		"-o", "use_ino",
	}
	if runtime.GOOS == "darwin" {
		flags = append(flags,
			"-o", "defer_permissions",
			"-o", "fsname=qrypt",
			"-o", "subtype=qrypt",
			"-o", "iosize=1048576",
		)
	}
	if runtime.GOOS == "windows" {
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
	if opts.VolumeName != "" {
		flags = append(flags, "-o", "volname="+opts.VolumeName)
	}
	return flags
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


