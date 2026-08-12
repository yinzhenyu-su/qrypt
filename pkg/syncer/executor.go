package sync

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/drivecopy"
)

// ExecutePlan runs the plan in dependency order: directories before the files
// inside them, and deletes before adds/updates when --conflict=source
// executorFS is the read-write surface the sync executor needs. It is
// deliberately narrower than vfs.FileSystem: sync never starts a filesystem
// and cache refresh is an implementation detail of the VFS.
type executorFS interface {
	vfs.Reader
	vfs.Writer
}

// replaced a type conflict. When session is non-nil, ops already finished OK
// in the session journal are skipped and every completed op is recorded; a
// journal persistence failure counts the op as failed so the session is kept
// for a util. waitIdle drains async VFS work between passes. Returns the
// number of failed items.
func ExecutePlan(ctx context.Context, fs executorFS, plan []PlanEntry, source, destination Target, session *Session, waitIdle func(context.Context, any) error) int {
	failed := 0
	// Pass 1: deletions (children before parents; plan is path-sorted, so
	// reverse order removes deeper paths first).
	for i := len(plan) - 1; i >= 0; i-- {
		entry := plan[i]
		if entry.Action != ActionDelete {
			continue
		}
		if session != nil && session.isDone(entry.Path, entry.Action) {
			continue
		}
		err := DeletePath(ctx, fs, entry.Path, destination, entry.IsDir)
		failed += recordOutcome(session, entry.Path, entry.Action, err)
	}
	// VFS deletes are queued and applied asynchronously; wait for them to
	// land before creating anything under the same name (--conflict=source
	// removes a path then re-adds it, which would race the removal).
	if err := waitIdle(ctx, fs); err != nil {
		ReportFailure("wait", "<deletes>", err)
		return failed + 1
	}
	// Pass 2: directories first, then files, so parents exist before children.
	for _, entry := range plan {
		if entry.Action != ActionAdd && entry.Action != ActionUpdate {
			continue
		}
		if !entry.IsDir {
			continue
		}
		if session != nil && session.isDone(entry.Path, entry.Action) {
			continue
		}
		err := Mkdir(ctx, fs, entry.Path, destination)
		failed += recordOutcome(session, entry.Path, entry.Action, err)
	}
	for _, entry := range plan {
		if entry.Action != ActionAdd && entry.Action != ActionUpdate {
			continue
		}
		if entry.IsDir {
			continue
		}
		if session != nil && session.isDone(entry.Path, entry.Action) {
			continue
		}
		err := CopyFile(ctx, fs, entry.Path, source, destination, entry.SourceModTime)
		failed += recordOutcome(session, entry.Path, entry.Action, err)
	}
	return failed
}

// recordOutcome persists one op result (when a session is active) and
// reports it. The op counts as failed if the transfer failed OR the progress
// journal could not be written: an in-memory-only success would desync the
// session state from disk and lose progress on interruption.
func recordOutcome(session *Session, path string, action Action, err error) int {
	if session != nil {
		if perr := session.markDone(path, action, err); perr != nil {
			ReportFailure("persist", path, perr)
			return 1
		}
	}
	if err != nil {
		ReportFailure(string(action), path, err)
		return 1
	}
	return 0
}

// DeletePath removes one entry on the destination side.
func DeletePath(ctx context.Context, fs executorFS, rel string, destination Target, isDir bool) error {
	switch destination.Kind {
	case TargetVFS:
		if isDir {
			return fs.RemoveDir(ctx, JoinVFS(destination.VFSPath, rel))
		}
		return fs.Remove(ctx, JoinVFS(destination.VFSPath, rel))
	default:
		local := OSPath(destination.LocalPath, rel)
		if isDir {
			return os.RemoveAll(local)
		}
		return os.Remove(local)
	}
}

// Mkdir ensures the destination directory exists.
func Mkdir(ctx context.Context, fs executorFS, rel string, destination Target) error {
	switch destination.Kind {
	case TargetVFS:
		if _, err := fs.Stat(ctx, JoinVFS(destination.VFSPath, rel)); err == nil {
			return nil
		}
		_, err := fs.Mkdir(ctx, JoinVFS(destination.VFSPath, rel))
		return err
	default:
		return os.MkdirAll(OSPath(destination.LocalPath, rel), 0o755)
	}
}

// CopyFile transfers one file from source to destination, then applies the
// source mtime to the destination so repeated syncs converge instead of
// re-uploading every file whose backend mtime is the upload time.
func CopyFile(ctx context.Context, fs executorFS, rel string, source, destination Target, sourceModTime int64) error {
	srcPath := JoinVFS(source.VFSPath, rel)
	dstPath := JoinVFS(destination.VFSPath, rel)
	switch {
	case source.Kind == TargetVFS && destination.Kind == TargetVFS:
		copier, ok := fs.(diagnostics.DriverCopySource)
		if !ok {
			return fmt.Errorf("direct copy requires a filesystem with driver debug resolution")
		}
		// Propagate the source mtime through the driver copy so a
		// destination backend that can persist it (CapabilityMtime)
		// converges on the next run; other backends ignore the stamp.
		result := drivecopy.RunDirectDriverCopyWithModTime(ctx, copier, srcPath, dstPath, true, UnixModTime(sourceModTime))
		if !result.Pass {
			return fmt.Errorf("direct copy: %s", drivecopy.DriverCopyError(result))
		}
		return nil
	case source.Kind == TargetLocal && destination.Kind == TargetVFS:
		if err := PutFile(ctx, fs, OSPath(source.LocalPath, rel), dstPath); err != nil {
			return err
		}
		return SetModTime(ctx, fs, dstPath, sourceModTime)
	case source.Kind == TargetVFS && destination.Kind == TargetLocal:
		if err := GetFile(ctx, fs, srcPath, OSPath(destination.LocalPath, rel)); err != nil {
			return err
		}
		// Copy the source mtime onto the local file so repeated syncs see a
		// stable destination.
		if sourceModTime > 0 {
			_ = os.Chtimes(OSPath(destination.LocalPath, rel), time.Now(), UnixModTime(sourceModTime))
		}
		return nil
	}
	return fmt.Errorf("unsupported sync pair: %v -> %v", source.Kind, destination.Kind)
}

// SetModTime applies the source mtime to a VFS path when the filesystem
// supports it; backends without SetModTime simply skip (size/hash still
// converge).
func SetModTime(ctx context.Context, fs executorFS, path string, modTime int64) error {
	if modTime <= 0 {
		return nil
	}
	setter, ok := fs.(vfs.ModTimeWriter)
	if !ok {
		return nil
	}
	return setter.SetModTime(ctx, path, UnixModTime(modTime))
}
