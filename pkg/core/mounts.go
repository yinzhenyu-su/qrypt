package core

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/subtree"
)

// MountRequest asks a Core to host one FUSE session for a configured view.
type MountRequest struct {
	// Name selects a configured mount subtree (/NAME); empty means the whole
	// namespace root.
	Name       string `json:"name,omitempty"`
	MountPoint string `json:"mount_point"`
	ReadOnly   bool   `json:"read_only,omitempty"`
}

// HostedMount is the observable state of one hosted FUSE session.
type HostedMount struct {
	ID         string    `json:"id"`
	Name       string    `json:"name,omitempty"`
	MountPoint string    `json:"mount_point"`
	ReadOnly   bool      `json:"read_only,omitempty"`
	StartedAt  time.Time `json:"started_at"`
}

// MountSession is a FUSE session whose kernel callbacks are served by this
// process. Done is closed when the session ends (external unmount, kernel
// teardown or a failed mount after Mount returned).
type MountSession interface {
	ID() string
	MountPoint() string
	Done() <-chan struct{}
}

// MountHost mounts a filesystem view on the host. The desktop composition root
// supplies the FUSE implementation. pkg/core must not import pkg/mount: that
// would drag cgofuse into embedded builds such as the gomobile library.
type MountHost interface {
	Mount(ctx context.Context, fs vfs.FileSystem, req MountRequest) (MountSession, error)
	Unmount(ctx context.Context, session MountSession) error
}

type mountEntry struct {
	req     MountRequest
	session MountSession
	info    HostedMount
}

// mountManager owns every FUSE session hosted by a Core. All mutations of the
// registry happen under mu; Mount/Unmount calls happen outside it so a slow
// mount never blocks listing.
type mountManager struct {
	mu     sync.Mutex
	host   MountHost
	mounts map[string]*mountEntry
	closed bool
	seq    uint64
}

func newMountManager(host MountHost) *mountManager {
	if host == nil {
		return nil
	}
	return &mountManager{host: host, mounts: map[string]*mountEntry{}}
}

func (m *mountManager) attach(ctx context.Context, fs vfs.FileSystem, req MountRequest) (HostedMount, error) {
	if m == nil {
		return HostedMount{}, fmt.Errorf("core: mounting is not enabled for this runtime")
	}
	req.MountPoint = strings.TrimSpace(req.MountPoint)
	if req.MountPoint == "" || !filepath.IsAbs(req.MountPoint) {
		return HostedMount{}, fmt.Errorf("core: mount point must be an absolute path")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return HostedMount{}, fmt.Errorf("core: closed")
	}
	for _, entry := range m.mounts {
		if entry.info.MountPoint == req.MountPoint {
			m.mu.Unlock()
			return HostedMount{}, fmt.Errorf("core: %s is already mounted", req.MountPoint)
		}
	}
	m.mu.Unlock()

	// Mount outside the lock; a second attach may race us for the same mount
	// point and is rejected after the fact below.
	view, err := subtree.New(fs, req.Name)
	if err != nil {
		return HostedMount{}, err
	}
	session, err := m.host.Mount(ctx, view, req)
	if err != nil {
		return HostedMount{}, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = m.host.Unmount(context.Background(), session)
		return HostedMount{}, fmt.Errorf("core: closed")
	}
	for _, entry := range m.mounts {
		if entry.info.MountPoint == req.MountPoint {
			m.mu.Unlock()
			_ = m.host.Unmount(context.Background(), session)
			return HostedMount{}, fmt.Errorf("core: %s is already mounted", req.MountPoint)
		}
	}
	m.seq++
	id := session.ID()
	if id == "" {
		id = fmt.Sprintf("mount-%d", m.seq)
	}
	info := HostedMount{
		ID:         id,
		Name:       req.Name,
		MountPoint: req.MountPoint,
		ReadOnly:   req.ReadOnly,
		StartedAt:  time.Now().UTC(),
	}
	m.mounts[id] = &mountEntry{req: req, session: session, info: info}
	m.mu.Unlock()

	go m.watch(id, session)
	return info, nil
}

// watch removes an entry when its FUSE session ends for any reason, so a
// kernel-initiated unmount does not leave a phantom mount behind.
func (m *mountManager) watch(id string, session MountSession) {
	done := session.Done()
	if done == nil {
		return
	}
	<-done
	m.mu.Lock()
	if entry, ok := m.mounts[id]; ok && entry.session == session {
		delete(m.mounts, id)
	}
	m.mu.Unlock()
}

func (m *mountManager) detach(ctx context.Context, id string) error {
	if m == nil {
		return fmt.Errorf("core: mounting is not enabled for this runtime")
	}
	m.mu.Lock()
	entry, ok := m.mounts[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("core: mount %q not found", id)
	}
	delete(m.mounts, id)
	m.mu.Unlock()
	return m.host.Unmount(ctx, entry.session)
}

func (m *mountManager) list() []HostedMount {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	infos := make([]HostedMount, 0, len(m.mounts))
	for _, entry := range m.mounts {
		infos = append(infos, entry.info)
	}
	m.mu.Unlock()
	sort.Slice(infos, func(i, j int) bool { return infos[i].MountPoint < infos[j].MountPoint })
	return infos
}

func (m *mountManager) closeAll(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	entries := make([]*mountEntry, 0, len(m.mounts))
	for _, entry := range m.mounts {
		entries = append(entries, entry)
	}
	m.mounts = map[string]*mountEntry{}
	m.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
	}
	for _, entry := range entries {
		if err := m.host.Unmount(ctx, entry.session); err != nil {
			logging.L.Warnf("[CORE] detach mount %s: %v", entry.info.MountPoint, err)
		}
	}
}

// AttachMount mounts a view of the core filesystem through the injected
// MountHost. A named view requires the core to serve a namespace; embedded
// single-mount runtimes only expose their root.
func (c *Core) AttachMount(ctx context.Context, req MountRequest) (HostedMount, error) {
	if c == nil || c.fs == nil {
		return HostedMount{}, fmt.Errorf("core: closed")
	}
	if req.Name != "" {
		if _, ok := c.mountNames[req.Name]; !ok {
			return HostedMount{}, fmt.Errorf("core: mount %q not found", req.Name)
		}
		if _, ok := c.fs.(*vfs.Namespace); !ok {
			return HostedMount{}, fmt.Errorf("core: this runtime serves a single mount and cannot host subtree views")
		}
	}
	return c.mounts.attach(ctx, c.fs, req)
}

// DetachMount unmounts one hosted FUSE session by id (its mount point).
func (c *Core) DetachMount(ctx context.Context, id string) error {
	if c == nil {
		return fmt.Errorf("core: closed")
	}
	return c.mounts.detach(ctx, id)
}

// ListMounts returns a snapshot of the hosted FUSE sessions.
func (c *Core) ListMounts() []HostedMount {
	if c == nil {
		return nil
	}
	return c.mounts.list()
}

func configuredMountNames(cfgMounts []string) map[string]struct{} {
	names := make(map[string]struct{}, len(cfgMounts))
	for _, name := range cfgMounts {
		names[name] = struct{}{}
	}
	return names
}

// coreMountService adapts the mount manager to the transport-facing contract.
type coreMountService struct {
	core *Core
}

func (s coreMountService) ListMounts(context.Context) ([]control.MountStatus, error) {
	infos := s.core.ListMounts()
	statuses := make([]control.MountStatus, 0, len(infos))
	for _, info := range infos {
		statuses = append(statuses, control.MountStatus{
			ID:         info.ID,
			Name:       info.Name,
			MountPoint: info.MountPoint,
			ReadOnly:   info.ReadOnly,
			StartedAt:  info.StartedAt,
		})
	}
	return statuses, nil
}

func (s coreMountService) AttachMount(ctx context.Context, req control.MountAttachRequest) (control.MountStatus, error) {
	info, err := s.core.AttachMount(ctx, MountRequest{Name: req.Name, MountPoint: req.MountPoint, ReadOnly: req.ReadOnly})
	if err != nil {
		return control.MountStatus{}, err
	}
	return control.MountStatus{
		ID:         info.ID,
		Name:       info.Name,
		MountPoint: info.MountPoint,
		ReadOnly:   info.ReadOnly,
		StartedAt:  info.StartedAt,
	}, nil
}

func (s coreMountService) DetachMount(ctx context.Context, id string) error {
	return s.core.DetachMount(ctx, id)
}
