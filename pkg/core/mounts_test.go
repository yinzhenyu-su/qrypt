package core

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type fakeFileSystem struct{}

func (fakeFileSystem) Stat(context.Context, string) (drive.Entry, error) { return drive.Entry{}, nil }
func (fakeFileSystem) List(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}
func (fakeFileSystem) Read(context.Context, string, int64, int64) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (fakeFileSystem) Create(context.Context, string) error { return nil }
func (fakeFileSystem) WriteAt(context.Context, string, []byte, int64) (int, error) {
	return 0, errors.New("not implemented")
}
func (fakeFileSystem) Flush(context.Context, string) error                { return nil }
func (fakeFileSystem) Mkdir(context.Context, string) (drive.Entry, error) { return drive.Entry{}, nil }
func (fakeFileSystem) Remove(context.Context, string) error               { return nil }
func (fakeFileSystem) RemoveDir(context.Context, string) error            { return nil }
func (fakeFileSystem) Rename(context.Context, string, string) error       { return nil }
func (fakeFileSystem) Truncate(context.Context, string, int64) error      { return nil }

type fakeSession struct {
	id         string
	mountPoint string
	done       chan struct{}
}

func (s *fakeSession) ID() string            { return s.id }
func (s *fakeSession) MountPoint() string    { return s.mountPoint }
func (s *fakeSession) Done() <-chan struct{} { return s.done }
func (s *fakeSession) finish()               { close(s.done) }

type fakeMountHost struct {
	mounted   []*fakeSession
	unmounted []string
	mountErr  error
}

func (h *fakeMountHost) Mount(_ context.Context, _ vfs.FileSystem, req MountRequest) (MountSession, error) {
	if h.mountErr != nil {
		return nil, h.mountErr
	}
	s := &fakeSession{id: "id-" + filepath.Base(req.MountPoint), mountPoint: req.MountPoint, done: make(chan struct{})}
	h.mounted = append(h.mounted, s)
	return s, nil
}

func (h *fakeMountHost) Unmount(_ context.Context, session MountSession) error {
	h.unmounted = append(h.unmounted, session.ID())
	s := session.(*fakeSession)
	select {
	case <-s.done:
	default:
		s.finish()
	}
	return nil
}

func newTestManager() *mountManager {
	return newMountManager(&fakeMountHost{})
}

func waitMounts(t *testing.T, m *mountManager, want int) []HostedMount {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := m.list()
		if len(got) == want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("mounts = %+v, want %d entries", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMountManagerAttachListDetach(t *testing.T) {
	m := newTestManager()
	ctx := context.Background()
	fs := fakeFileSystem{}
	mountPoint := filepath.Join(t.TempDir(), "mnt")

	info, err := m.attach(ctx, fs, MountRequest{MountPoint: mountPoint})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if info.ID == "" || info.MountPoint != mountPoint || info.StartedAt.IsZero() {
		t.Fatalf("info = %+v", info)
	}
	got := waitMounts(t, m, 1)
	if got[0].ID != info.ID {
		t.Fatalf("list = %+v, want id %q", got, info.ID)
	}

	if _, err := m.attach(ctx, fs, MountRequest{MountPoint: mountPoint}); err == nil {
		t.Fatal("duplicate mount point was accepted")
	}
	if _, err := m.attach(ctx, fs, MountRequest{MountPoint: "relative"}); err == nil {
		t.Fatal("relative mount point was accepted")
	}

	if err := m.detach(ctx, info.ID); err != nil {
		t.Fatalf("detach: %v", err)
	}
	waitMounts(t, m, 0)
	if err := m.detach(ctx, info.ID); err == nil {
		t.Fatal("detaching an unknown mount succeeded")
	}
}

func TestMountManagerExternalUnmountRemovesEntry(t *testing.T) {
	host := &fakeMountHost{}
	m := newMountManager(host)
	info, err := m.attach(context.Background(), fakeFileSystem{}, MountRequest{MountPoint: filepath.Join(t.TempDir(), "a")})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	waitMounts(t, m, 1)
	host.mounted[0].finish()
	waitMounts(t, m, 0)
	if info.ID == "" {
		t.Fatal("empty id")
	}
}

func TestMountManagerCloseAll(t *testing.T) {
	host := &fakeMountHost{}
	m := newMountManager(host)
	ctx := context.Background()
	base := t.TempDir()
	if _, err := m.attach(ctx, fakeFileSystem{}, MountRequest{MountPoint: filepath.Join(base, "a")}); err != nil {
		t.Fatalf("attach a: %v", err)
	}
	if _, err := m.attach(ctx, fakeFileSystem{}, MountRequest{MountPoint: filepath.Join(base, "b")}); err != nil {
		t.Fatalf("attach b: %v", err)
	}
	m.closeAll(ctx)
	if got := m.list(); len(got) != 0 {
		t.Fatalf("list after close = %+v", got)
	}
	if len(host.unmounted) != 2 {
		t.Fatalf("unmounted = %v, want 2", host.unmounted)
	}
	if _, err := m.attach(ctx, fakeFileSystem{}, MountRequest{MountPoint: filepath.Join(base, "c")}); err == nil {
		t.Fatal("attach after close succeeded")
	}
}

func TestCoreBusyCountsHostedMounts(t *testing.T) {
	manager := newMountManager(&fakeMountHost{})
	c := &Core{mounts: manager}
	if c.Busy() {
		t.Fatal("core without mounts or tasks should be idle")
	}
	hosted, err := manager.attach(context.Background(), fakeFileSystem{}, MountRequest{MountPoint: filepath.Join(t.TempDir(), "mnt")})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if !c.Busy() {
		t.Fatal("core with a hosted mount must stay busy so it cannot idle-exit")
	}
	if err := manager.detach(context.Background(), hosted.ID); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if c.Busy() {
		t.Fatal("core should be idle again after the mount is gone")
	}
}
