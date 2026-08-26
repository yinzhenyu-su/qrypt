package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/mutation"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"time"
)

type vfsDriverRuntime struct {
	driver      drive.Driver
	testEnabled bool
}

func newVFSDriverRuntime(driver drive.Driver, testEnabled bool) vfsDriverRuntime {
	return vfsDriverRuntime{driver: driver, testEnabled: testEnabled}
}

func (r vfsDriverRuntime) Capabilities() []drive.Capability {
	return drive.Capabilities(r.driver)
}

func (r vfsDriverRuntime) HasCapability(capability drive.Capability) bool {
	return drive.HasCapability(r.driver, capability)
}

func (r vfsDriverRuntime) RequireCapability(capability drive.Capability, operation string) error {
	if r.HasCapability(capability) {
		return nil
	}
	return fmt.Errorf("vfs: driver does not support %s", operation)
}

func (r vfsDriverRuntime) MutationBackend() mutation.Backend {
	return mutation.NewDriverBackend(r.driver)
}

func (r vfsDriverRuntime) RemoteMutationBackend() upload.DriverBackend {
	return upload.NewDriverBackend(r.driver)
}

func (r vfsDriverRuntime) NamedDriver(name string) diagnostics.NamedDriver {
	return diagnostics.NamedDriver{Name: name, Driver: r.driver, TestEnabled: r.testEnabled}
}

func (r vfsDriverRuntime) Encrypted() bool {
	return diagnostics.DriverMarkedEncrypted(r.driver)
}

func (r vfsDriverRuntime) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	return r.driver.DebugSnapshot(ctx)
}

func (r vfsDriverRuntime) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return r.driver.Metrics(ctx, since)
}

func (r vfsDriverRuntime) Space(ctx context.Context) (drive.Space, error) {
	if err := r.RequireCapability(drive.CapabilitySpace, "space query"); err != nil {
		return drive.Space{}, err
	}
	return r.driver.Space(ctx)
}

func (r vfsDriverRuntime) ResolveRemoteName(ctx context.Context, plainName string) (drive.RemoteNameInfo, error) {
	if err := r.RequireCapability(drive.CapabilityRemoteNameResolver, "remote name resolver"); err != nil {
		return drive.RemoteNameInfo{}, err
	}
	return r.driver.ResolveRemoteName(ctx, plainName)
}

func (r vfsDriverRuntime) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return r.driver.List(ctx, parentID)
}

func (r vfsDriverRuntime) ForeignEntries(ctx context.Context, parentID string) ([]drive.ForeignEntry, error) {
	if err := r.RequireCapability(drive.CapabilityForeignEntries, "foreign entries"); err != nil {
		return nil, err
	}
	return r.driver.ForeignEntries(ctx, parentID)
}

func (r vfsDriverRuntime) Remove(ctx context.Context, entry drive.Entry) error {
	return r.driver.Remove(ctx, entry)
}

func (r vfsDriverRuntime) RequiredUploadHashes() []drive.HashAlgorithm {
	if r.driver == nil {
		return nil
	}
	return r.driver.RequiredUploadHashes()
}
