// Package sync implements the fs sync domain: tree snapshots, comparison,
// planning, execution and resumable sessions. The CLI layer only parses
// arguments into a Request and formats the Result; everything else lives
// here so the sync core is testable without cobra or a full command tree.
package sync

import (
	"context"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

// TargetKind classifies one side of a sync as virtual (VFS mount) or local.
type TargetKind int

const (
	TargetLocal TargetKind = iota
	TargetVFS
)

// Target is one side of a sync pair. Local targets have LocalPath; virtual
// targets have VFSPath (and the mount name for driver lookups).
type Target struct {
	Kind      TargetKind
	Raw       string
	VFSPath   string
	LocalPath string
	MountName string
	Encrypted bool
}

// CheckContainment rejects a destination nested inside its own source.
// Only virtual↔virtual pairs can overlap this way.
func CheckContainment(source, destination Target) error {
	if source.Kind != TargetVFS || destination.Kind != TargetVFS {
		return nil
	}
	if destination.MountName != source.MountName {
		return nil
	}
	src := pathpkg.Clean(source.VFSPath)
	dst := pathpkg.Clean(destination.VFSPath)
	if src == dst {
		return fmt.Errorf("SOURCE and DESTINATION are the same path")
	}
	if strings.HasPrefix(dst+"/", src+"/") {
		return fmt.Errorf("DESTINATION %q is inside SOURCE %q", destination.Raw, source.Raw)
	}
	return nil
}

// TargetSupportsMTime reports whether the destination backend persists
// uploaded mtimes. Local destinations always do; virtual destinations
// depend on the mount driver's CapabilityMtime.
func TargetSupportsMTime(fs any, destination Target) bool {
	if destination.Kind != TargetVFS {
		return true
	}
	provider, ok := fs.(diagnostics.DriverProvider)
	if !ok {
		return true
	}
	for _, nd := range provider.Drivers() {
		if nd.Name == destination.MountName {
			return drive.HasCapability(nd.Driver, drive.CapabilityMtime)
		}
	}
	return true
}

// EnsureRoot creates the destination root when it does not exist yet, so the
// sync plan (which excludes the root itself) can place entries in it.
func EnsureRoot(ctx context.Context, fs executorFS, destination Target) error {
	switch destination.Kind {
	case TargetVFS:
		if _, err := fs.Stat(ctx, destination.VFSPath); err == nil {
			return nil
		}
		_, err := fs.Mkdir(ctx, destination.VFSPath)
		return err
	default:
		return os.MkdirAll(destination.LocalPath, 0o755)
	}
}

// JoinVFS joins a VFS root with a relative slash path.
func JoinVFS(base, rel string) string {
	return pathpkg.Join(base, rel)
}

// OSPath joins a local root with a relative slash path.
func OSPath(root, rel string) string {
	return filepath.Join(root, filepath.FromSlash(rel))
}
