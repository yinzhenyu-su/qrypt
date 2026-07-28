package vfs

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type remoteMutationBackend interface {
	CanWrite() bool
	List(ctx context.Context, parentID string) ([]drive.Entry, error)
	PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error)
	Remove(ctx context.Context, entry drive.Entry) error
	Rename(ctx context.Context, entry drive.Entry, newName string) error
}

type driverRemoteMutationBackend struct {
	driver drive.Driver
}

func newDriverRemoteMutationBackend(driver drive.Driver) driverRemoteMutationBackend {
	return driverRemoteMutationBackend{driver: driver}
}

func (b driverRemoteMutationBackend) CanWrite() bool {
	return drive.HasCapability(b.driver, drive.CapabilityWriter)
}

func (b driverRemoteMutationBackend) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return b.driver.List(ctx, parentID)
}

func (b driverRemoteMutationBackend) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	return b.driver.PutSource(ctx, req)
}

func (b driverRemoteMutationBackend) Remove(ctx context.Context, entry drive.Entry) error {
	return b.driver.Remove(ctx, entry)
}

func (b driverRemoteMutationBackend) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	return b.driver.Rename(ctx, entry, newName)
}
