package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// syncAction is one step of a sync plan.
type syncAction string

const (
	syncAdd      syncAction = "add"
	syncUpdate   syncAction = "update"
	syncDelete   syncAction = "delete"
	syncSkip     syncAction = "skip"
	syncConflict syncAction = "conflict"
)

// syncPlanEntry is one planned or executed sync step.
type syncPlanEntry struct {
	Path       string     `json:"path"`
	Action     syncAction `json:"action"`
	Reason     string     `json:"reason,omitempty"`
	IsDir      bool       `json:"is_dir,omitempty"`
	SourceSize int64      `json:"source_size,omitempty"`
	DestSize   int64      `json:"dest_size,omitempty"`
	Bytes      int64      `json:"bytes,omitempty"`
	// SourceModTime carries the authoritative mtime so the destination can
	// be set to it after transfer; otherwise the backend stamps the upload
	// time and every subsequent sync sees an mtime difference.
	SourceModTime int64 `json:"source_mod_time,omitempty"`
}

// syncSummary aggregates a plan or run by action.
type syncSummary struct {
	Add      int   `json:"add"`
	Update   int   `json:"update"`
	Delete   int   `json:"delete"`
	Skip     int   `json:"skip"`
	Conflict int   `json:"conflict"`
	Failed   int   `json:"failed"`
	Bytes    int64 `json:"bytes"`
}

// syncResult is the machine-readable output of a sync run.
type syncResult struct {
	OK          bool            `json:"ok"`
	Source      string          `json:"source"`
	Destination string          `json:"destination"`
	DryRun      bool            `json:"dry_run"`
	Summary     syncSummary     `json:"summary"`
	Entries     []syncPlanEntry `json:"entries"`
}

func newFsSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync SOURCE DESTINATION",
		Short: "Make DESTINATION match SOURCE (one-way, non-destructive by default)",
		Long: `Synchronize the tree at DESTINATION to match the tree at SOURCE.

SOURCE is authoritative; DESTINATION is modified. Files missing on
DESTINATION are added, changed files are updated, and extra files on
DESTINATION are left alone unless --delete is given. Type conflicts
(file vs directory on the same path) are reported and skipped by
default; use --conflict=source to let SOURCE win.

At least one side must be a virtual path (/MOUNT/dir). Local paths are
compared against the VFS plaintext view (encryption is transparent).

Exit code: 0 on success (also for --dry-run plans), 3 when some files
failed, 2 on usage errors, 1 on overall failures.`,
		Args:              exactNamedArgs("SOURCE", "DESTINATION"),
		RunE:              runFsSync,
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Bool("dry-run", false, "show the sync plan without changing anything")
	cmd.Flags().Bool("delete", false, "delete files on DESTINATION that are not on SOURCE")
	cmd.Flags().Bool("hash", false, "also compare content hashes (needs backend hash support)")
	cmd.Flags().String("conflict", "error", "type-conflict policy: error, skip, or source")
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func runFsSync(cmd *cobra.Command, args []string) error {
	state, err := commandConfig(cmd)
	if err != nil {
		return err
	}
	if state.cfg == nil {
		return configNotFoundError()
	}
	source := resolveCheckTarget(state.cfg, args[0])
	destination := resolveCheckTarget(state.cfg, args[1])
	if source.kind == targetLocal && destination.kind == targetLocal {
		return commandUsageError(cmd, "at least one side must be a virtual path (/MOUNT/...): got %q and %q", args[0], args[1])
	}
	conflictPolicy, _ := cmd.Flags().GetString("conflict")
	switch conflictPolicy {
	case "error", "skip", "source":
	default:
		return commandUsageError(cmd, "--conflict must be error, skip, or source (got %q)", conflictPolicy)
	}
	// A destination nested under its own source would recurse forever.
	if err := checkSyncContainment(source, destination); err != nil {
		return commandUsageError(cmd, "%v", err)
	}

	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	forceHash, _ := cmd.Flags().GetBool("hash")
	snapA, err := snapshotTarget(ctx, fs, source)
	if err != nil {
		return err
	}
	// The destination may not exist yet; sync treats it as an empty tree so
	// the whole tree is added (mkdir happens during execution).
	snapB, err := snapshotTarget(ctx, fs, destination)
	if err != nil && !errors.Is(err, vfs.ErrNotFound) && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if snapB == nil {
		snapB = treeSnapshot{}
	}
	// Sync always attempts content-hash comparison; AutoHash degrades to
	// size/mtime when the backends cannot provide hashes. --hash forces it
	// (missing hash support becomes an error).
	opts := treeCompareOptions{AsHash: true, AutoHash: !forceHash}
	opts.Hash = func(ctx context.Context, rel string) (bool, string, error) {
		return compareVFSHashPair(ctx, fs, source, destination, rel, opts.AutoHash)
	}
	diffs, err := compareTrees(ctx, snapA, snapB, opts)
	if err != nil {
		return err
	}

	deleteExtra, _ := cmd.Flags().GetBool("delete")
	plan := planSync(diffs, snapA, snapB, deleteExtra, conflictPolicy, targetSupportsMTime(fs, destination))
	result := syncResult{
		Source:      args[0],
		Destination: args[1],
		Entries:     plan,
	}
	for _, entry := range plan {
		result.Summary.add(entry)
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	result.DryRun = dryRun
	if !dryRun {
		if err := ensureSyncRoot(ctx, fs, destination); err != nil {
			return fmt.Errorf("create sync destination: %w", err)
		}
		failed := executeSyncPlan(ctx, fs, plan, source, destination)
		result.Summary.Failed = failed
		// Wait for async VFS uploads to land so the destination is actually
		// synced when the command returns.
		if err := waitFileSystemIdle(ctx, fs, commandWaitTimeout(cmd)); err != nil {
			return err
		}
	}
	result.OK = result.Summary.Failed == 0

	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		if err := writePrettyJSON(cmd.OutOrStdout(), result); err != nil {
			return err
		}
	} else {
		printSyncSummary(cmd.OutOrStdout(), result)
	}
	if result.Summary.Failed > 0 {
		return &ExitError{Code: ExitPartial, Err: fmt.Errorf("sync finished with %d failed item(s)", result.Summary.Failed)}
	}
	if result.Summary.Conflict > 0 && conflictPolicy == "error" {
		return &ExitError{Code: ExitPartial, Err: fmt.Errorf("sync finished with %d type conflict(s); use --conflict=skip or --conflict=source", result.Summary.Conflict)}
	}
	return nil
}

func (s *syncSummary) add(entry syncPlanEntry) {
	switch entry.Action {
	case syncAdd:
		s.Add++
		s.Bytes += entry.Bytes
	case syncUpdate:
		s.Update++
		s.Bytes += entry.Bytes
	case syncDelete:
		s.Delete++
	case syncSkip:
		s.Skip++
	case syncConflict:
		s.Conflict++
	}
}

// checkSyncContainment rejects a destination nested inside its own source.
// Only virtual↔virtual pairs can overlap this way.
func checkSyncContainment(source, destination checkTarget) error {
	if source.kind != targetVFS || destination.kind != targetVFS {
		return nil
	}
	if destination.mountName != source.mountName {
		return nil
	}
	src := pathpkg.Clean(source.vfsPath)
	dst := pathpkg.Clean(destination.vfsPath)
	if src == dst {
		return fmt.Errorf("SOURCE and DESTINATION are the same path")
	}
	if strings.HasPrefix(dst+"/", src+"/") {
		return fmt.Errorf("DESTINATION %q is inside SOURCE %q", destination.raw, source.raw)
	}
	return nil
}

// planSync maps tree differences to sync actions. Source is authoritative;
// destination extras become skips unless deleteExtra is set.
func planSync(diffs []treeDifference, snapA, snapB treeSnapshot, deleteExtra bool, conflictPolicy string, compareMTime bool) []syncPlanEntry {
	var plan []syncPlanEntry
	conflictAsAdd := conflictPolicy == "source"
	for _, d := range diffs {
		entryA := snapA[d.Path]
		entryB := snapB[d.Path]
		switch d.Reason {
		case "missing_in_b":
			plan = append(plan, syncPlanEntry{
				Path: d.Path, Action: syncAdd, Reason: "missing",
				IsDir: d.IsDir, SourceSize: entryA.Size, Bytes: entryA.Size,
				SourceModTime: entryA.ModTime,
			})
		case "size", "mtime", "hash":
			if d.Reason == "mtime" && !compareMTime {
				// The destination backend cannot persist mtime (it stamps the
				// upload time), so an mtime difference would never converge:
				// skip the no-op touch and rely on size/hash comparison.
				continue
			}
			plan = append(plan, syncPlanEntry{
				Path: d.Path, Action: syncUpdate, Reason: d.Reason,
				IsDir: d.IsDir, SourceSize: entryA.Size, DestSize: entryB.Size,
				Bytes:         diffBytes(entryA.Size, entryB.Size),
				SourceModTime: entryA.ModTime,
			})
		case "extra_in_b":
			if deleteExtra {
				plan = append(plan, syncPlanEntry{Path: d.Path, Action: syncDelete, Reason: "extra", IsDir: d.IsDir})
			} else {
				plan = append(plan, syncPlanEntry{Path: d.Path, Action: syncSkip, Reason: "extra", IsDir: d.IsDir, DestSize: entryB.Size})
			}
		case "type":
			if conflictAsAdd {
				// SOURCE wins: delete the destination entry, then add.
				plan = append(plan, syncPlanEntry{Path: d.Path, Action: syncDelete, Reason: "type", IsDir: entryB.IsDir, DestSize: entryB.Size})
				plan = append(plan, syncPlanEntry{Path: d.Path, Action: syncAdd, Reason: "type", IsDir: entryA.IsDir, SourceSize: entryA.Size, Bytes: entryA.Size})
			} else {
				plan = append(plan, syncPlanEntry{Path: d.Path, Action: syncConflict, Reason: "type", IsDir: entryA.IsDir})
			}
		}
	}
	return plan
}

func diffBytes(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

// executeSyncPlan runs the plan in dependency order: directories before the
// files inside them, and deletes before adds/updates when --conflict=source
// replaced a type conflict. Returns the number of failed items.
func executeSyncPlan(ctx context.Context, fs vfs.FileSystem, plan []syncPlanEntry, source, destination checkTarget) int {
	failed := 0
	// Pass 1: deletions (children before parents; plan is path-sorted, so
	// reverse order removes deeper paths first).
	for i := len(plan) - 1; i >= 0; i-- {
		entry := plan[i]
		if entry.Action != syncDelete {
			continue
		}
		if err := syncDeletePath(ctx, fs, entry.Path, destination, entry.IsDir); err != nil {
			reportSyncFailure("delete", entry.Path, err)
			failed++
		}
	}
	// Pass 2: directories first, then files, so parents exist before children.
	for _, entry := range plan {
		if entry.Action != syncAdd && entry.Action != syncUpdate {
			continue
		}
		if !entry.IsDir {
			continue
		}
		if err := syncMkdir(ctx, fs, entry.Path, destination); err != nil {
			reportSyncFailure("mkdir", entry.Path, err)
			failed++
		}
	}
	for _, entry := range plan {
		if entry.Action != syncAdd && entry.Action != syncUpdate {
			continue
		}
		if entry.IsDir {
			continue
		}
		if err := syncCopyFile(ctx, fs, entry.Path, source, destination, entry.SourceModTime); err != nil {
			reportSyncFailure(string(entry.Action), entry.Path, err)
			failed++
		}
	}
	return failed
}

// targetSupportsMTime reports whether the destination backend persists
// uploaded mtimes. Local destinations always do; virtual destinations
// depend on the mount driver's CapabilityMtime.
func targetSupportsMTime(fs vfs.FileSystem, destination checkTarget) bool {
	if destination.kind != targetVFS {
		return true
	}
	provider, ok := fs.(vfs.DriverProvider)
	if !ok {
		return true
	}
	for _, nd := range provider.Drivers() {
		if nd.Name == destination.mountName {
			return drive.HasCapability(nd.Driver, drive.CapabilityMtime)
		}
	}
	return true
}

// ensureSyncRoot creates the destination root when it does not exist yet,
// so the sync plan (which excludes the root itself) can place entries in it.
func ensureSyncRoot(ctx context.Context, fs vfs.FileSystem, destination checkTarget) error {
	switch destination.kind {
	case targetVFS:
		if _, err := fs.Stat(ctx, destination.vfsPath); err == nil {
			return nil
		}
		if _, err := fs.Mkdir(ctx, destination.vfsPath); err != nil {
			return err
		}
		return nil
	default:
		return os.MkdirAll(destination.localPath, 0o755)
	}
}

// syncDeletePath removes one entry on the destination side.
func syncDeletePath(ctx context.Context, fs vfs.FileSystem, rel string, destination checkTarget, isDir bool) error {
	switch destination.kind {
	case targetVFS:
		if isDir {
			return fs.RemoveDir(ctx, joinVFS(destination.vfsPath, rel))
		}
		return fs.Remove(ctx, joinVFS(destination.vfsPath, rel))
	default:
		local := osPath(destination.localPath, rel)
		if isDir {
			return os.RemoveAll(local)
		}
		return os.Remove(local)
	}
}

// syncMkdir ensures the destination directory exists.
func syncMkdir(ctx context.Context, fs vfs.FileSystem, rel string, destination checkTarget) error {
	switch destination.kind {
	case targetVFS:
		if _, err := fs.Stat(ctx, joinVFS(destination.vfsPath, rel)); err == nil {
			return nil
		}
		_, err := fs.Mkdir(ctx, joinVFS(destination.vfsPath, rel))
		return err
	default:
		return os.MkdirAll(osPath(destination.localPath, rel), 0o755)
	}
}

// syncCopyFile transfers one file from source to destination, then applies
// the source mtime to the destination so repeated syncs converge instead of
// re-uploading every file whose backend mtime is the upload time.
func syncCopyFile(ctx context.Context, fs vfs.FileSystem, rel string, source, destination checkTarget, sourceModTime int64) error {
	srcPath := joinVFS(source.vfsPath, rel)
	dstPath := joinVFS(destination.vfsPath, rel)
	switch {
	case source.kind == targetVFS && destination.kind == targetVFS:
		copier, ok := fs.(control.DriverCopySource)
		if !ok {
			return fmt.Errorf("direct copy requires a filesystem with driver debug resolution")
		}
		result := control.RunDirectDriverCopy(ctx, copier, srcPath, dstPath, true)
		if !result.Pass {
			return fmt.Errorf("direct copy: %s", control.DriverCopyError(result))
		}
		return nil
	case source.kind == targetLocal && destination.kind == targetVFS:
		if err := put(ctx, fs, osPath(source.localPath, rel), dstPath); err != nil {
			return err
		}
		return syncSetModTime(ctx, fs, dstPath, sourceModTime)
	case source.kind == targetVFS && destination.kind == targetLocal:
		if err := get(ctx, fs, srcPath, osPath(destination.localPath, rel), true, true); err != nil {
			return err
		}
		// Copy the source mtime onto the local file so repeated syncs see a
		// stable destination.
		if sourceModTime > 0 {
			_ = os.Chtimes(osPath(destination.localPath, rel), time.Now(), time.Unix(sourceModTime, 0))
		}
		return nil
	}
	return fmt.Errorf("unsupported sync pair: %v -> %v", source.kind, destination.kind)
}

// syncSetModTime applies the source mtime to a VFS path when the filesystem
// supports it; backends without SetModTime simply skip (size/hash still
// converge).
func syncSetModTime(ctx context.Context, fs vfs.FileSystem, path string, modTime int64) error {
	if modTime <= 0 {
		return nil
	}
	setter, ok := fs.(interface {
		SetModTime(ctx context.Context, path string, modTime time.Time) error
	})
	if !ok {
		return nil
	}
	return setter.SetModTime(ctx, path, time.Unix(modTime, 0))
}

func reportSyncFailure(op, path string, err error) {
	fmt.Fprintf(os.Stderr, "sync %s %s: %v\n", op, path, err)
}

func printSyncSummary(w interface {
	Write([]byte) (int, error)
}, result syncResult) {
	verb := "would sync"
	if !result.DryRun {
		verb = "synced"
	}
	fmt.Fprintf(w, "%s %s -> %s\n", verb, result.Source, result.Destination)
	fmt.Fprintf(w, "  add: %d, update: %d, delete: %d, skip: %d, conflict: %d, failed: %d\n",
		result.Summary.Add, result.Summary.Update, result.Summary.Delete,
		result.Summary.Skip, result.Summary.Conflict, result.Summary.Failed)
	if !result.DryRun || result.Summary.Bytes > 0 {
		fmt.Fprintf(w, "  bytes: %d\n", result.Summary.Bytes)
	}
	for _, entry := range result.Entries {
		if entry.Action == syncSkip && result.DryRun {
			continue
		}
		detail := entry.Reason
		if entry.Bytes > 0 {
			detail = fmt.Sprintf("%s (%d bytes)", detail, entry.Bytes)
		}
		fmt.Fprintf(w, "  [%s] %s %s\n", entry.Action, entry.Path, detail)
	}
}
