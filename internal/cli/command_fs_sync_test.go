package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/internal/sync"
)

func setupSyncTest(t *testing.T) (configPath, remote, local string) {
	t.Helper()
	configPath, remote, local = setupCheckTest(t)
	// Every sync test must keep its session store out of the real config
	// dir; partial-failure runs deliberately keep sessions on disk.
	t.Setenv("QRYPT_SYNC_DIR", filepath.Join(t.TempDir(), "qrypt-sync"))
	return configPath, remote, local
}

func syncSummaryOf(t *testing.T, out string) sync.Summary {
	t.Helper()
	var result sync.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal sync result: %v out=%s", err, out)
	}
	return result.Summary
}

// TestFsSyncIdenticalTreesEmptyPlan: identical trees produce an empty plan.
func TestFsSyncIdenticalTreesEmptyPlan(t *testing.T) {
	configPath, remote, local := setupSyncTest(t)
	copyTree(t, remote, local)

	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--json", "/loc", local)
	if err != nil {
		t.Fatalf("sync identical failed: %v", err)
	}
	summary := syncSummaryOf(t, out)
	if summary.Adds != 0 || summary.Update != 0 || summary.Failed != 0 {
		t.Fatalf("identical trees produced changes: %+v", summary)
	}
}

// TestFsSyncAddsMissingFilesLocalToVFS: a file present only on the local
// side is added to the VFS and its content propagates.
func TestFsSyncAddsMissingFilesLocalToVFS(t *testing.T) {
	configPath, remote, local := setupSyncTest(t)
	// Remove remote copy so the local file is "missing" on the VFS side.
	if err := os.Remove(filepath.Join(remote, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "new.txt"), []byte("sync-new-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--json", local, "/loc")
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	summary := syncSummaryOf(t, out)
	if summary.Adds < 1 || summary.Failed != 0 {
		t.Fatalf("summary = %+v, want at least one add with no failures", summary)
	}

	// The added file must be readable back through the VFS with the right
	// content (sync round-trips through the overlay).
	got, _, err := executeCLI(t, "fs", "--config", configPath, "cat", "/loc/new.txt")
	if err != nil {
		t.Fatalf("cat added file: %v", err)
	}
	if !strings.Contains(got, "sync-new-content") {
		t.Fatalf("added file content = %q, want sync-new-content", got)
	}
}

// TestFsSyncUpdatesChangedFiles: a size-changed file on the source side is
// updated on the destination.
func TestFsSyncUpdatesChangedFiles(t *testing.T) {
	configPath, _, local := setupSyncTest(t)
	// Local copy differs in size from the VFS copy.
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("local-larger-content-here"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--json", local, "/loc")
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	summary := syncSummaryOf(t, out)
	if summary.Update < 1 || summary.Failed != 0 {
		t.Fatalf("summary = %+v, want updates with no failures", summary)
	}

	got, _, err := executeCLI(t, "fs", "--config", configPath, "cat", "/loc/a.txt")
	if err != nil {
		t.Fatalf("cat updated file: %v", err)
	}
	if !strings.Contains(got, "local-larger-content-here") {
		t.Fatalf("updated file content = %q", got)
	}
}

// TestFsSyncDoesNotDeleteByDefault: extra files on the destination survive
// unless --delete is given.
func TestFsSyncDoesNotDeleteByDefault(t *testing.T) {
	configPath, _, local := setupSyncTest(t)
	if err := os.WriteFile(filepath.Join(local, "orphan.txt"), []byte("keep-me"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--json", "/loc", local); err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(local, "orphan.txt")); err != nil {
		t.Fatalf("orphan was deleted without --delete: %v", err)
	}
}

// TestFsSyncDeleteFlagRemovesExtra: --delete removes destination extras.
func TestFsSyncDeleteFlagRemovesExtra(t *testing.T) {
	configPath, _, local := setupSyncTest(t)
	if err := os.WriteFile(filepath.Join(local, "orphan.txt"), []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--delete", "--json", "/loc", local)
	if err != nil {
		t.Fatalf("sync --delete failed: %v", err)
	}
	summary := syncSummaryOf(t, out)
	if summary.Delete < 1 {
		t.Fatalf("summary = %+v, want deletes", summary)
	}
	if _, err := os.Stat(filepath.Join(local, "orphan.txt")); !os.IsNotExist(err) {
		t.Fatalf("orphan still exists after --delete: %v", err)
	}
}

// TestFsSyncDryRunWritesNothing: a dry run produces a plan but no writes.
func TestFsSyncDryRunWritesNothing(t *testing.T) {
	configPath, remote, local := setupSyncTest(t)
	if err := os.WriteFile(filepath.Join(local, "new.txt"), []byte("dry-run-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--dry-run", "--json", "/loc", local)
	if err != nil {
		t.Fatalf("sync --dry-run failed (exit must be 0): %v", err)
	}
	summary := syncSummaryOf(t, out)
	if summary.Adds < 1 {
		t.Fatalf("dry-run summary = %+v, want adds", summary)
	}
	// Nothing must have been written to the remote side.
	if _, err := os.Stat(filepath.Join(remote, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote to remote: %v", err)
	}
}

// TestFsSyncTypeConflictFails: file/directory type conflicts are reported
// and never silently overwritten (default policy error → exit 3).
func TestFsSyncTypeConflictFails(t *testing.T) {
	configPath, remote, _ := setupSyncTest(t)
	if err := os.Remove(filepath.Join(remote, "sub", "b.txt")); err != nil {
		t.Fatal(err)
	}
	// VFS side: sub/item is a file. Local side: sub/item is a directory.
	if err := os.WriteFile(filepath.Join(remote, "sub", "item"), []byte("file-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "local")
	if err := os.MkdirAll(filepath.Join(local, "sub", "item"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "sub", "item", "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--json", "/loc", local)
	var xe *ExitError
	if err == nil || !errors.As(err, &xe) || xe.Code != ExitPartial {
		t.Fatalf("type conflict err = %v, want ExitPartial(3)", err)
	}
	summary := syncSummaryOf(t, out)
	if summary.Conflict < 1 {
		t.Fatalf("summary = %+v, want conflicts", summary)
	}
	// The JSON ok flag must agree with the exit code (conflicts under the
	// error policy are not success).
	var result sync.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.OK {
		t.Fatalf("ok=true with %d conflicts under --conflict=error", summary.Conflict)
	}
	// The destination directory must be untouched.
	if _, statErr := os.Stat(filepath.Join(local, "sub", "item", "x.txt")); statErr != nil {
		t.Fatalf("conflict overwrote destination: %v", statErr)
	}
}

// TestFsSyncConflictSourceWins: --conflict=source lets the source replace a
// type conflict (destination directory is removed, file takes its place).
func TestFsSyncConflictSourceWins(t *testing.T) {
	configPath, remote, _ := setupSyncTest(t)
	if err := os.Remove(filepath.Join(remote, "sub", "b.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "sub", "item"), []byte("file-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "local")
	if err := os.MkdirAll(filepath.Join(local, "sub", "item", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "sub", "item", "nested", "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--conflict=source", "--json", "/loc", local)
	if err != nil {
		t.Fatalf("sync --conflict=source failed: %v", err)
	}
	got, _, err := executeCLI(t, "fs", "--config", configPath, "cat", "/loc/sub/item")
	if err != nil {
		t.Fatalf("source-won file missing: %v", err)
	}
	if !strings.Contains(got, "file-data") {
		t.Fatalf("source file content = %q, want file-data", got)
	}
}

// TestFsSyncJSONSorted: JSON entries are sorted by path for stable output.
func TestFsSyncJSONSorted(t *testing.T) {
	configPath, remote, local := setupSyncTest(t)
	if err := os.Remove(filepath.Join(remote, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "zebra.txt"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "alpha.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--dry-run", "--json", "/loc", local)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	var result sync.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		paths = append(paths, entry.Path)
	}
	for i := 1; i < len(paths); i++ {
		if paths[i-1] >= paths[i] {
			t.Fatalf("entries not sorted: %v", paths)
		}
	}
}

// TestFsSyncPartialFailureExits3: one unreadable source file fails without
// blocking the rest, and the run exits 3.
func TestFsSyncPartialFailureExits3(t *testing.T) {
	configPath, remote, local := setupSyncTest(t)
	if err := os.Remove(filepath.Join(remote, "a.txt")); err != nil {
		t.Fatal(err)
	}
	// A broken symlink is unreadable by the uploader: its add fails, the
	// other file still syncs.
	if err := os.Symlink(filepath.Join(local, "no-such-target"), filepath.Join(local, "broken.txt")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, "good.txt"), []byte("good"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--json", local, "/loc")
	var xe *ExitError
	if err == nil || !errors.As(err, &xe) || xe.Code != ExitPartial {
		t.Fatalf("partial failure err = %v, want ExitPartial(3)", err)
	}
	summary := syncSummaryOf(t, out)
	if summary.Failed < 1 || summary.Adds < 1 {
		t.Fatalf("summary = %+v, want both a failed item and successful adds", summary)
	}
	// The good file must have been synced despite the failure.
	got, _, err := executeCLI(t, "fs", "--config", configPath, "cat", "/loc/good.txt")
	if err != nil {
		t.Fatalf("good file not synced: %v", err)
	}
	if !strings.Contains(got, "good") {
		t.Fatalf("good file content = %q", got)
	}
}

// TestFsSyncVFSToVFS: a file missing in a second VFS directory is added via
// the direct driver copy path.
func TestFsSyncVFSToVFS(t *testing.T) {
	configPath, remote, _ := setupSyncTest(t)
	// Build a source subtree in /loc/src and an empty destination /loc/dst.
	if err := os.MkdirAll(filepath.Join(remote, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "src", "f.txt"), []byte("vfs-to-vfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(remote, "dst"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--json", "/loc/src", "/loc/dst")
	if err != nil {
		t.Fatalf("vfs->vfs sync failed: %v", err)
	}
	summary := syncSummaryOf(t, out)
	if summary.Adds < 1 || summary.Failed != 0 {
		t.Fatalf("summary = %+v, want adds with no failures", summary)
	}
	got, _, err := executeCLI(t, "fs", "--config", configPath, "cat", "/loc/dst/f.txt")
	if err != nil {
		t.Fatalf("copied file missing: %v", err)
	}
	if !strings.Contains(got, "vfs-to-vfs") {
		t.Fatalf("copied content = %q", got)
	}
}

// TestFsSyncVFSToVFSConverges: the source mtime must propagate through the
// direct driver copy, so a second sync sees no update instead of re-copying
// every file whose destination mtime differs.
func TestFsSyncVFSToVFSConverges(t *testing.T) {
	configPath, remote, _ := setupSyncTest(t)
	if err := os.MkdirAll(filepath.Join(remote, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "src", "f.txt"), []byte("vfs-to-vfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(remote, "dst"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "/loc/src", "/loc/dst"); err != nil {
		t.Fatalf("first sync failed: %v", err)
	}
	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--json", "/loc/src", "/loc/dst")
	if err != nil {
		t.Fatalf("second sync failed: %v", err)
	}
	summary := syncSummaryOf(t, out)
	if summary.Update != 0 || summary.Adds != 0 || summary.Failed != 0 {
		t.Fatalf("second sync summary = %+v, want convergence (no updates)", summary)
	}
}

// TestFsSyncConvergesOnSecondRun: after a sync, the source mtime is
// propagated to the VFS destination, so a second sync sees no differences
// instead of re-uploading every file.
func TestFsSyncConvergesOnSecondRun(t *testing.T) {
	configPath, remote, local := setupSyncTest(t)
	if err := os.Remove(filepath.Join(remote, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "new.txt"), []byte("converge"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := executeCLI(t, "fs", "--config", configPath, "sync", local, "/loc"); err != nil {
		t.Fatalf("first sync failed: %v", err)
	}
	// Second sync: nothing changed, so the plan must be empty.
	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--json", local, "/loc")
	if err != nil {
		t.Fatalf("second sync failed: %v", err)
	}
	summary := syncSummaryOf(t, out)
	if summary.Adds != 0 || summary.Update != 0 || summary.Delete != 0 || summary.Failed != 0 {
		t.Fatalf("second sync not converged: %+v", summary)
	}
}

// TestFsSyncCompareMtimeOnlySkipsSizeOnlyChange: a local file whose size
// changed but whose mtime matches the source is ignored under
// --compare=mtime-only, while the default strategy updates it.
func TestFsSyncCompareMtimeOnlySkipsSizeOnlyChange(t *testing.T) {
	configPath, remote, local := setupSyncTest(t)
	copyTree(t, remote, local)
	remoteInfo, err := os.Stat(filepath.Join(remote, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	st := remoteInfo.ModTime()
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("local-size-only-change"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(local, "a.txt"), st, st); err != nil {
		t.Fatal(err)
	}

	// Default strategy sees the size difference and updates.
	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--json", local, "/loc")
	if err != nil {
		t.Fatalf("default sync failed: %v", err)
	}
	if summary := syncSummaryOf(t, out); summary.Update < 1 || summary.Failed != 0 {
		t.Fatalf("default sync summary = %+v, want update", summary)
	}

	// mtime-only treats the pair as identical: no update.
	out, _, err = executeCLI(t, "fs", "--config", configPath, "sync", "--json", "--compare=mtime-only", local, "/loc")
	if err != nil {
		t.Fatalf("mtime-only sync failed: %v", err)
	}
	if summary := syncSummaryOf(t, out); summary.Adds != 0 || summary.Update != 0 || summary.Delete != 0 || summary.Failed != 0 {
		t.Fatalf("mtime-only sync summary = %+v, want empty plan", summary)
	}
}

func TestFsSyncCompareInvalidMode(t *testing.T) {
	configPath, _, local := setupSyncTest(t)
	_, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--compare=bogus", local, "/loc")
	if err == nil || !strings.Contains(err.Error(), "invalid --compare") {
		t.Fatalf("invalid --compare err = %v, want validation error", err)
	}
}
