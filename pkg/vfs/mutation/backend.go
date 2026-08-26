package mutation

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Backend is the full remote mutation surface a VFS mount exercises for
// rename/mkdir coordination: child listing, mkdir, and the single-remote
// rename/move ops (Remote) the transactional renamer drives.
type Backend interface {
	List(ctx context.Context, parentID string) ([]drive.Entry, error)
	Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error)
	Rename(ctx context.Context, entry drive.Entry, newName string) error
	Move(ctx context.Context, entry drive.Entry, dstParentID string) error
}

// DriverBackend adapts a drive.Driver to Backend (and thus to Remote and
// MkdirRemote) for a VFS mount.
type DriverBackend struct {
	driver drive.Driver
}

// NewDriverBackend wraps a driver for the mutation coordinators.
func NewDriverBackend(driver drive.Driver) DriverBackend {
	return DriverBackend{driver: driver}
}

func (b DriverBackend) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return b.driver.List(ctx, parentID)
}

func (b DriverBackend) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	return b.driver.Mkdir(ctx, parentID, name)
}

func (b DriverBackend) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	return b.driver.Rename(ctx, entry, newName)
}

func (b DriverBackend) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	return b.driver.Move(ctx, entry, dstParentID)
}

var (
	_ Backend     = DriverBackend{}
	_ Remote      = DriverBackend{}
	_ MkdirRemote = DriverBackend{}
)
