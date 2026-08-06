package sync

import (
	"os"
	"path/filepath"
	"testing"
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
