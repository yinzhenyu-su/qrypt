package diagnostics

import (
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// MountAllowed reports whether name matches the requested mount filter;
// an empty filter allows every mount.
func MountAllowed(mountName string, mountNames []string) bool {
	if len(mountNames) == 0 {
		return true
	}
	mountName = vfstypes.CleanMountName(mountName)
	for _, candidate := range mountNames {
		if vfstypes.CleanMountName(strings.TrimSpace(candidate)) == mountName {
			return true
		}
	}
	return false
}

// MountNameSet builds the normalized mount-name filter set.
func MountNameSet(mountNames []string) map[string]bool {
	set := map[string]bool{}
	for _, name := range mountNames {
		name = vfstypes.CleanMountName(name)
		if name != "" {
			set[name] = true
		}
	}
	return set
}

// DriverMarkedEncrypted reports whether a live driver advertises encryption
// through the optional Encrypted marker. Distinct from DriverEncrypted,
// which reads the flag from a driver debug snapshot instead.
func DriverMarkedEncrypted(driver drive.Driver) bool {
	marker, ok := driver.(interface{ Encrypted() bool })
	return ok && marker.Encrypted()
}
