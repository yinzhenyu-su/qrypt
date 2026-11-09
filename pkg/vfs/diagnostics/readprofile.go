package diagnostics

import "github.com/yinzhenyu/qrypt/pkg/drive"

// ReadProfileProvider exposes the optional per-mount read profile override for
// a virtual path. A nil result means the mount keeps the driver default.
type ReadProfileProvider interface {
	ReadProfileForPath(path string) *drive.ReadProfile
}

// MountReadProfileProvider exposes the optional per-mount read profile override
// by mount name. A nil result means the mount keeps the driver default.
type MountReadProfileProvider interface {
	ReadProfileForMount(mount string) *drive.ReadProfile
}
