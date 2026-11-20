package core

import (
	"context"
	"fmt"

	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	corepkg "github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/mount"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// fuseMountSession adapts mount.Session to the core-owned MountSession
// contract so pkg/core never imports pkg/mount (and cgofuse).
type fuseMountSession struct {
	session *mount.Session
}

func (s fuseMountSession) ID() string            { return s.session.ID }
func (s fuseMountSession) MountPoint() string    { return s.session.MountPoint }
func (s fuseMountSession) Done() <-chan struct{} { return s.session.Done() }

// fuseMountHost hosts FUSE sessions inside the resident Core process. Mount
// options come from the Core's configuration; an attach request may only
// choose the mount point, the view and read-only mode.
type fuseMountHost struct {
	rt  cliruntime.Runtime
	cfg *config.Config
}

func newFuseMountHost(rt cliruntime.Runtime, cfg *config.Config) *fuseMountHost {
	return &fuseMountHost{rt: rt, cfg: cfg}
}

func (h *fuseMountHost) Mount(ctx context.Context, fs vfs.FileSystem, req corepkg.MountRequest) (corepkg.MountSession, error) {
	opts, err := h.rt.MountOptionsFromConfig(h.cfg)
	if err != nil {
		return nil, err
	}
	opts.MountPoint = req.MountPoint
	opts.ReadOnly = opts.ReadOnly || req.ReadOnly
	opts.Foreground = true
	opts.UnmountOnError = true
	session, err := mount.NewMounter().Mount(ctx, fs, opts)
	if err != nil {
		return nil, fmt.Errorf("mount %s: %w", req.MountPoint, err)
	}
	return fuseMountSession{session: session}, nil
}

func (h *fuseMountHost) Unmount(ctx context.Context, session corepkg.MountSession) error {
	owned, ok := session.(fuseMountSession)
	if !ok {
		return fmt.Errorf("core: unexpected mount session type %T", session)
	}
	return mount.NewMounter().Unmount(ctx, owned.session)
}
