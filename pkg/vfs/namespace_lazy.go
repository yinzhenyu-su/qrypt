package vfs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// LazyMount describes a configured mount whose VFS is opened on first access.
// Cold starts use lazy mounts so a command that touches one mount does not pay
// the initialization cost (network Init, root resolve) of every other mount.
type LazyMount struct {
	Name string
	Open func(context.Context) (*VFS, error)
}

type lazyMount struct {
	mu   sync.Mutex
	fs   *VFS
	err  error
	open func(context.Context) (*VFS, error)
}

// resolve opens the mount once and registers it with the namespace before
// returning, so concurrent readers observe the mount through the normal map.
func (l *lazyMount) resolve(ctx context.Context, register func(*VFS)) (*VFS, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fs != nil {
		return l.fs, nil
	}
	if l.err != nil {
		return nil, l.err
	}
	fs, err := l.open(ctx)
	if err != nil {
		l.err = err
		return nil, err
	}
	register(fs)
	l.fs = fs
	return fs, nil
}

// NewLazyNamespace builds a namespace whose mounts are opened on demand. The
// configured names are visible to root listing immediately; each mount is
// initialized (and started) the first time a path under it is accessed.
func NewLazyNamespace(mounts []LazyMount, failures []MountFailure) (*Namespace, error) {
	ns := &Namespace{
		mounts:    map[string]*VFS{},
		lazy:      map[string]*lazyMount{},
		subs:      map[*invalidationSubscription]struct{}{},
		failures:  append([]MountFailure(nil), failures...),
		createdAt: time.Now(),
	}
	for _, mount := range mounts {
		name := vfstypes.CleanMountName(mount.Name)
		if name == "" {
			return nil, fmt.Errorf("vfs: mount name required")
		}
		if _, exists := ns.lazy[name]; exists {
			return nil, fmt.Errorf("vfs: duplicate mount name %q", name)
		}
		if mount.Open == nil {
			return nil, fmt.Errorf("vfs: mount %q: nil opener", name)
		}
		ns.lazy[name] = &lazyMount{open: mount.Open}
	}
	if len(ns.lazy) == 0 {
		return nil, fmt.Errorf("vfs: namespace requires at least one mount")
	}
	return ns, nil
}
