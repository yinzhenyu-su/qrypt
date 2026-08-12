package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/syncer"
)

// setupSyncPersistTest returns a config + local dir pair whose sync sessions
// are isolated under a temp QRYPT_SYNC_DIR.
func setupSyncPersistTest(t *testing.T) (configPath, remote, local string) {
	t.Helper()
	configPath, remote, local = setupSyncTest(t)
	t.Setenv("QRYPT_SYNC_DIR", filepath.Join(t.TempDir(), "qrypt-sync"))
	return configPath, remote, local
}

// syncPersistDir returns the session directory for the given CLI args.
func syncPersistDir(t *testing.T, source, destination string) string {
	t.Helper()
	// The session key is a pure function of the two descriptors.
	return filepath.Join(sync.PersistRoot(), sync.SessionKey(
		sync.Target{Kind: sync.TargetLocal, Raw: source, LocalPath: source},
		sync.Target{Kind: sync.TargetVFS, Raw: destination, VFSPath: destination, MountName: "loc"},
	))
}

func writeSyncSessionPlan(t *testing.T, source, destination sync.Target, ops []sync.PlanEntry) {
	t.Helper()
	dir := filepath.Join(sync.PersistRoot(), sync.SessionKey(source, destination))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := sync.SessionPlan{
		Version:     1,
		Source:      sync.TargetDescriptor(source),
		Destination: sync.TargetDescriptor(destination),
		Flags:       sync.SessionFlags{Delete: false, Hash: false, Conflict: "error"},
		Ops:         ops,
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendSyncState(t *testing.T, dir, path string, action sync.Action, ok bool) {
	t.Helper()
	entry := sync.StateEntry{Op: "done", Path: path, Action: action, OK: ok}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "state.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

// TestFsSyncResumeWithoutSessionFails: --resume with no saved session for
// the pair is a usage error, not a silent no-op.
func TestFsSyncResumeWithoutSessionFails(t *testing.T) {
	configPath, _, local := setupSyncPersistTest(t)
	_, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--resume", "--json", local, "/loc")
	var xe *ExitError
	if err == nil || !errors.As(err, &xe) || xe.Code != ExitUsage {
		t.Fatalf("resume without session err = %v, want ExitUsage(2)", err)
	}
}

// TestFsSyncResumeCompletesInterruptedRun: a session whose plan recorded
// three adds but only one finished OK resumes the other two, and the
// finished op is not re-transferred.
func TestFsSyncResumeCompletesInterruptedRun(t *testing.T) {
	configPath, remote, local := setupSyncPersistTest(t)
	// Source holds three files; the destination only has one (from the
	// interrupted first run).
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "b.txt"), []byte("bbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "c.txt"), []byte("ccc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Hand-craft the interrupted session: plan has all three adds, state
	// says a.txt finished OK.
	writeSyncSessionPlan(t,
		sync.Target{Kind: sync.TargetLocal, Raw: local, LocalPath: local},
		sync.Target{Kind: sync.TargetVFS, Raw: "/loc", VFSPath: "/loc", MountName: "loc"},
		[]sync.PlanEntry{
			{Path: "a.txt", Action: sync.ActionAdd, Reason: "missing", SourceSize: 3, Bytes: 3},
			{Path: "b.txt", Action: sync.ActionAdd, Reason: "missing", SourceSize: 3, Bytes: 3},
			{Path: "c.txt", Action: sync.ActionAdd, Reason: "missing", SourceSize: 3, Bytes: 3},
		})
	appendSyncState(t, syncPersistDir(t, local, "/loc"), "a.txt", sync.ActionAdd, true)

	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--resume", "--json", local, "/loc")
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	summary := syncSummaryOf(t, out)
	if summary.Adds != 2 || summary.Failed != 0 {
		t.Fatalf("summary = %+v, want 2 adds and no failures", summary)
	}

	// Both resumed files landed; the already-finished one is untouched.
	for name, want := range map[string]string{"a.txt": "aaa", "b.txt": "bbb", "c.txt": "ccc"} {
		got, _, err := executeCLI(t, "fs", "--config", configPath, "cat", "/loc/"+name)
		if err != nil {
			t.Fatalf("cat %s: %v", name, err)
		}
		if !strings.Contains(got, want) {
			t.Fatalf("%s content = %q, want %q", name, got, want)
		}
	}
	// The session completed, so the directory is gone.
	if _, err := os.Stat(syncPersistDir(t, local, "/loc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session dir still exists after completion: %v", err)
	}
}

// TestFsSyncResumeRetriesFailedOp: an op recorded as failed in the journal
// is retried by resume.
func TestFsSyncResumeRetriesFailedOp(t *testing.T) {
	configPath, _, local := setupSyncPersistTest(t)
	if err := os.WriteFile(filepath.Join(local, "f.txt"), []byte("fixed-now"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Session: f.txt marked failed. Resume must retry it.
	writeSyncSessionPlan(t,
		sync.Target{Kind: sync.TargetLocal, Raw: local, LocalPath: local},
		sync.Target{Kind: sync.TargetVFS, Raw: "/loc", VFSPath: "/loc", MountName: "loc"},
		[]sync.PlanEntry{
			{Path: "f.txt", Action: sync.ActionAdd, Reason: "missing", SourceSize: 10, Bytes: 10},
		})
	appendSyncState(t, syncPersistDir(t, local, "/loc"), "f.txt", sync.ActionAdd, false)

	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--resume", "--json", local, "/loc")
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	summary := syncSummaryOf(t, out)
	if summary.Adds != 1 || summary.Failed != 0 {
		t.Fatalf("summary = %+v, want 1 add retried without failure", summary)
	}
	got, _, err := executeCLI(t, "fs", "--config", configPath, "cat", "/loc/f.txt")
	if err != nil {
		t.Fatalf("retried file missing: %v", err)
	}
	if !strings.Contains(got, "fixed-now") {
		t.Fatalf("retried content = %q", got)
	}
	if _, err := os.Stat(syncPersistDir(t, local, "/loc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session dir still exists after completion: %v", err)
	}
}

// TestFsSyncSessionRemovedOnSuccess: a clean run leaves no session behind.
func TestFsSyncSessionRemovedOnSuccess(t *testing.T) {
	configPath, remote, local := setupSyncPersistTest(t)
	if err := os.Remove(filepath.Join(remote, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "new.txt"), []byte("clean-run"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := executeCLI(t, "fs", "--config", configPath, "sync", local, "/loc"); err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	entries, err := os.ReadDir(sync.PersistRoot())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("sync left %d session dirs behind", len(entries))
	}
}

// TestFsSyncSessionSurvivesFailureAndResumes: a partially failed run keeps
// its session; a resume with the broken file fixed completes and removes it.
func TestFsSyncSessionSurvivesFailureAndResumes(t *testing.T) {
	configPath, remote, local := setupSyncPersistTest(t)
	if err := os.Remove(filepath.Join(remote, "a.txt")); err != nil {
		t.Fatal(err)
	}
	// A broken symlink makes its own add fail.
	if err := os.Symlink(filepath.Join(local, "no-such-target"), filepath.Join(local, "broken.txt")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, "good.txt"), []byte("good"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--json", local, "/loc")
	var xe *ExitError
	if err == nil || !errors.As(err, &xe) || xe.Code != ExitPartial {
		t.Fatalf("partial failure err = %v, want ExitPartial(3)", err)
	}
	entries, err := os.ReadDir(sync.PersistRoot())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("partial run left %d session dirs, want 1", len(entries))
	}

	// Fix the broken file (replace the dangling symlink with a real file),
	// then resume: only it should be re-attempted.
	if err := os.Remove(filepath.Join(local, "broken.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "broken.txt"), []byte("now-real"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionName := entries[0].Name()
	out, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--resume", "--json", local, "/loc")
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	summary := syncSummaryOf(t, out)
	if summary.Failed != 0 {
		t.Fatalf("resume summary = %+v, want no failures", summary)
	}
	// Session dir removed after completion.
	dir := filepath.Join(sync.PersistRoot(), sessionName)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session dir still exists after resume completion: %v", err)
	}
	got, _, err := executeCLI(t, "fs", "--config", configPath, "cat", "/loc/good.txt")
	if err != nil {
		t.Fatalf("good file lost: %v", err)
	}
	if !strings.Contains(got, "good") {
		t.Fatalf("good content = %q", got)
	}
}

// TestFsSyncDryRunWritesNoSession: dry-run must not touch the session store.
func TestFsSyncDryRunWritesNoSession(t *testing.T) {
	configPath, remote, local := setupSyncPersistTest(t)
	if err := os.Remove(filepath.Join(remote, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "new.txt"), []byte("dry"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := executeCLI(t, "fs", "--config", configPath, "sync", "--dry-run", "--json", local, "/loc"); err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}
	// dry-run must not create the session store at all.
	if _, err := os.Stat(sync.PersistRoot()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created the session store: %v", err)
	}
}
