package upload

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// DriverBackend adapts a drive.Driver to RemoteOps for the upload engine.
type DriverBackend struct {
	driver drive.Driver
}

// NewDriverBackend wraps a driver so the upload engine can reach the remote.
func NewDriverBackend(driver drive.Driver) DriverBackend {
	return DriverBackend{driver: driver}
}

func (b DriverBackend) CanWrite() bool {
	return drive.HasCapability(b.driver, drive.CapabilityWriter)
}

func (b DriverBackend) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return b.driver.List(ctx, parentID)
}

func (b DriverBackend) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	return b.driver.PutSource(ctx, req)
}

func (b DriverBackend) Remove(ctx context.Context, entry drive.Entry) error {
	return b.driver.Remove(ctx, entry)
}

func (b DriverBackend) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	return b.driver.Rename(ctx, entry, newName)
}

var _ RemoteOps = DriverBackend{}
