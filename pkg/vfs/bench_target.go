package vfs

import (
	"context"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// BenchTarget is one driver entry reachable through a virtual path. Probe
// benchmarks read it directly through the driver, so a read-only mount and a
// read-only driver can be measured without uploading a benchmark fixture.
type BenchTarget struct {
	Entry  drive.Entry
	Driver drive.Driver
}

// BenchTarget resolves virtualPath on mount to the driver entry and the driver
// that serve its bytes.
func (v *VFS) BenchTarget(ctx context.Context, mount, virtualPath string) (BenchTarget, error) {
	if mount != "" && mount != v.name {
		return BenchTarget{}, fmt.Errorf("vfs: mount %q not found", mount)
	}
	entry, err := v.Stat(ctx, virtualPath)
	if err != nil {
		return BenchTarget{}, err
	}
	return BenchTarget{Entry: entry, Driver: v.driver}, nil
}

// BenchTarget resolves virtualPath through the named mount of the namespace.
func (n *Namespace) BenchTarget(ctx context.Context, mount, virtualPath string) (BenchTarget, error) {
	name := vfstypes.CleanMountName(mount)
	if name == "" {
		return BenchTarget{}, fmt.Errorf("vfs: benchmark target requires a mount name")
	}
	mountFS, ok := n.mounts[name]
	if !ok {
		return BenchTarget{}, fmt.Errorf("vfs: mount %q not found", name)
	}
	return mountFS.BenchTarget(ctx, name, virtualPath)
}
