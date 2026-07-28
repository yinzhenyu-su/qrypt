package vfs

import (
	"context"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type vfsDriverRuntime struct {
	v *VFS
}

func newVFSDriverRuntime(v *VFS) vfsDriverRuntime {
	return vfsDriverRuntime{v: v}
}

func (r vfsDriverRuntime) Capabilities() []drive.Capability {
	return drive.Capabilities(r.v.driver)
}

func (r vfsDriverRuntime) HasCapability(capability drive.Capability) bool {
	return drive.HasCapability(r.v.driver, capability)
}

func (r vfsDriverRuntime) RequireCapability(capability drive.Capability, operation string) error {
	if r.HasCapability(capability) {
		return nil
	}
	return fmt.Errorf("vfs: driver does not support %s", operation)
}

func (r vfsDriverRuntime) ListBackend() listBackend {
	return newDriverListBackend(r.v.driver)
}

func (r vfsDriverRuntime) MutationBackend() mutationBackend {
	return newDriverMutationBackend(r.v.driver)
}

func (r vfsDriverRuntime) RemoteMutationBackend() remoteMutationBackend {
	return newDriverRemoteMutationBackend(r.v.driver)
}

func (r vfsDriverRuntime) NamedDriver(name string) NamedDriver {
	return NamedDriver{Name: name, Driver: r.v.driver, TestEnabled: r.v.testEnabled}
}

func (r vfsDriverRuntime) Encrypted() bool {
	return debugEncrypted(r.v.driver)
}

func (r vfsDriverRuntime) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	return r.v.driver.DebugSnapshot(ctx)
}

func (r vfsDriverRuntime) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return r.v.driver.Metrics(ctx, since)
}

func (r vfsDriverRuntime) Space(ctx context.Context) (drive.Space, error) {
	if err := r.RequireCapability(drive.CapabilitySpace, "space query"); err != nil {
		return drive.Space{}, err
	}
	return r.v.driver.Space(ctx)
}

func (r vfsDriverRuntime) ResolveRemoteName(ctx context.Context, plainName string) (drive.RemoteNameInfo, error) {
	if err := r.RequireCapability(drive.CapabilityRemoteNameResolver, "remote name resolver"); err != nil {
		return drive.RemoteNameInfo{}, err
	}
	return r.v.driver.ResolveRemoteName(ctx, plainName)
}

func (r vfsDriverRuntime) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return r.v.driver.List(ctx, parentID)
}

func (r vfsDriverRuntime) ForeignEntries(ctx context.Context, parentID string) ([]drive.ForeignEntry, error) {
	if err := r.RequireCapability(drive.CapabilityForeignEntries, "foreign entries"); err != nil {
		return nil, err
	}
	return r.v.driver.ForeignEntries(ctx, parentID)
}

func (r vfsDriverRuntime) Remove(ctx context.Context, entry drive.Entry) error {
	return r.v.driver.Remove(ctx, entry)
}

func (r vfsDriverRuntime) RequiredUploadHashes() []drive.HashAlgorithm {
	if r.v.driver == nil {
		return nil
	}
	return r.v.driver.RequiredUploadHashes()
}
