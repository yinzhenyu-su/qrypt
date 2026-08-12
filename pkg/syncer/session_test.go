package sync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionKeyIsDeterministic: the same pair always maps to the same key
// regardless of trailing slash or path spelling.
func TestSessionKeyIsDeterministic(t *testing.T) {
	src := func(p string) Target { return Target{Kind: TargetLocal, LocalPath: p} }
	dst := func(p string) Target { return Target{Kind: TargetVFS, VFSPath: p, MountName: "loc"} }
	if SessionKey(src("/tmp/x"), dst("/loc")) != SessionKey(src("/tmp/x/"), dst("/loc/")) {
		t.Fatal("session key changed with trailing slash")
	}
}

func TestPersistRootUsesQryptHome(t *testing.T) {
	home := useTestQryptHome(t)
	if got, want := PersistRoot(), filepath.Join(home, "qrypt-sync"); got != want {
		t.Fatalf("PersistRoot = %q, want %q", got, want)
	}
	override := filepath.Join(t.TempDir(), "sync-only")
	t.Setenv("QRYPT_SYNC_DIR", override)
	if got := PersistRoot(); got != override {
		t.Fatalf("PersistRoot with QRYPT_SYNC_DIR = %q, want %q", got, override)
	}
}

// TestMarkDonePersistenceFailure: a journal write failure must surface as an
// error and must NOT mark the op done in memory, so the session stays
// consistent with the disk and a resume retries the op.
func TestMarkDonePersistenceFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(dir, "state.jsonl")
	if err := os.WriteFile(journal, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(journal, 0o444); err != nil {
		t.Fatal(err)
	}
	p := &Session{dir: dir, done: map[string]bool{}}
	if err := p.markDone("a.txt", ActionAdd, nil); err == nil {
		t.Fatal("markDone on a read-only journal must fail")
	}
	if p.isDone("a.txt", ActionAdd) {
		t.Fatal("failed journal write must not mark the op done in memory")
	}
}

// TestSessionLifecycleRoundTrip exercises the full session state machine:
// create, progress, failed ops, reload, flag persistence and removal.
func TestSessionLifecycleRoundTrip(t *testing.T) {
	t.Setenv("QRYPT_SYNC_DIR", filepath.Join(t.TempDir(), "sync"))
	src := Target{Kind: TargetLocal, LocalPath: "/tmp/src"}
	dst := Target{Kind: TargetVFS, VFSPath: "/dest", MountName: "loc"}
	ops := []PlanEntry{
		{Path: "a.txt", Action: ActionAdd},
		{Path: "b.txt", Action: ActionAdd},
		{Path: "gone.txt", Action: ActionDelete},
	}
	s, err := NewSession(src, dst, SessionFlags{Delete: true, Conflict: "error"}, ops)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Remove()

	if got := s.PendingOps(); len(got) != 3 {
		t.Fatalf("initial pending = %d, want 3", len(got))
	}
	if !s.TransferPending() {
		t.Fatal("fresh session must have pending transfers")
	}
	if f := s.Flags(); !f.Delete || f.Conflict != "error" || f.Hash {
		t.Fatalf("flags = %+v, want delete+error", f)
	}

	// a.txt done, gone.txt deleted, b.txt fails (recorded, not done).
	if err := s.markDone("a.txt", ActionAdd, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.markDone("gone.txt", ActionDelete, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.markDone("b.txt", ActionAdd, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if s.isDone("b.txt", ActionAdd) {
		t.Fatal("failed op must not be marked done")
	}
	if !s.TransferPending() {
		t.Fatal("failed op must keep the session pending")
	}
	s.Close()

	// Reload: flags survive, done state survives, failed op is retryable.
	s2, found, err := LoadSession(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("session not found after Close")
	}
	defer s2.Close()
	if f := s2.Flags(); !f.Delete || f.Conflict != "error" {
		t.Fatalf("reloaded flags = %+v", f)
	}
	if !s2.isDone("a.txt", ActionAdd) || !s2.isDone("gone.txt", ActionDelete) {
		t.Fatal("reloaded session lost completed ops")
	}
	pending := s2.PendingOps()
	if len(pending) != 1 || pending[0].Path != "b.txt" {
		t.Fatalf("reloaded pending = %+v, want only b.txt", pending)
	}
	s2.Remove()
	if _, found, _ := LoadSession(src, dst); found {
		t.Fatal("removed session still loadable")
	}
}

// TestPruneExpiredReapsIdleSessions: a closed session older than the TTL is
// dropped; a session with a live lock survives even when old.
func TestPruneExpiredReapsIdleSessions(t *testing.T) {
	t.Setenv("QRYPT_SYNC_DIR", filepath.Join(t.TempDir(), "sync"))
	src := Target{Kind: TargetLocal, LocalPath: "/tmp/src"}
	dst := Target{Kind: TargetVFS, VFSPath: "/dest", MountName: "loc"}

	// Old idle session: created, closed, then aged past the TTL.
	old, err := NewSession(src, dst, SessionFlags{}, []PlanEntry{{Path: "x", Action: ActionAdd}})
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	oldDir := filepath.Join(PersistRoot(), SessionKey(src, dst))
	age := time.Now().Add(-(SessionTTL + time.Hour))
	if err := os.Chtimes(oldDir, age, age); err != nil {
		t.Fatal(err)
	}
	// Make every file inside old too, so newestFileTime is stale.
	entries, _ := os.ReadDir(oldDir)
	for _, e := range entries {
		_ = os.Chtimes(filepath.Join(oldDir, e.Name()), age, age)
	}

	// Locked session: same age but a live lock must protect it. A distinct
	// pair so it does not share the pruned directory.
	locked, err := NewSession(
		Target{Kind: TargetLocal, LocalPath: "/tmp/locked-src"}, dst,
		SessionFlags{}, []PlanEntry{{Path: "y", Action: ActionAdd}})
	if err != nil {
		t.Fatal(err)
	}
	lockedDir := locked.dir
	_ = os.Chtimes(lockedDir, age, age)

	PruneExpired()

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("idle expired session was not pruned (stat err: %v)", err)
	}
	if _, err := os.Stat(lockedDir); err != nil {
		t.Fatalf("locked session was pruned: %v", err)
	}
	locked.Close()
}
