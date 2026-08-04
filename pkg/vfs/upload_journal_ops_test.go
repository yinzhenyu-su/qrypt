package vfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeJournalEntry(t *testing.T, path string, op string, pending PendingUpload) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := json.Marshal(journalEntry{Op: op, PendingUpload: pending})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestReplayUploadJournalResetsFailures(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "pending.jsonl")
	stagingFile := filepath.Join(dir, "staging", "chunk-ok")
	if err := os.MkdirAll(filepath.Dir(stagingFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stagingFile, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Healthy entry must be left untouched.
	writeJournalEntry(t, journal, "dirty", PendingUpload{
		Path: "/ok.txt", FID: "f-ok", LocalPath: stagingFile, Size: 4,
	})
	// Failed entry: permanent failure + retry bookkeeping.
	failStaging := filepath.Join(dir, "staging", "chunk-fail")
	if err := os.WriteFile(failStaging, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJournalEntry(t, journal, "dirty", PendingUpload{
		Path: "/fail.txt", FID: "f-fail", LocalPath: failStaging, Size: 4,
		PermanentFail: true, LastError: "network down", RetryCount: 3,
		LastAttemptAt: 123, NextAttemptAt: 456,
	})

	replayed, err := ReplayUploadJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 || replayed[0].Path != "/fail.txt" {
		t.Fatalf("replayed = %+v, want only /fail.txt", replayed)
	}
	if replayed[0].PermanentFail || replayed[0].LastError != "" || replayed[0].RetryCount != 0 {
		t.Fatalf("replayed entry not reset: %+v", replayed[0])
	}

	// Re-loading the journal must show the reset state.
	store, err := newUploadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	all := store.PendingUploads()
	for _, p := range all {
		if p.Path == "/fail.txt" && (p.PermanentFail || p.LastError != "") {
			t.Fatalf("journal still holds failure state after replay: %+v", p)
		}
		if p.Path == "/ok.txt" && p.LastError != "" {
			t.Fatalf("healthy entry was touched: %+v", p)
		}
	}
}

func TestPruneUploadJournalDropsMissingStaging(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "pending.jsonl")
	keepStaging := filepath.Join(dir, "staging", "chunk-keep")
	if err := os.MkdirAll(filepath.Dir(keepStaging), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keepStaging, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJournalEntry(t, journal, "dirty", PendingUpload{
		Path: "/keep.txt", FID: "f-keep", LocalPath: keepStaging, Size: 4,
	})
	// Staging file never existed: entry must be pruned.
	writeJournalEntry(t, journal, "dirty", PendingUpload{
		Path: "/gone.txt", FID: "f-gone", LocalPath: filepath.Join(dir, "staging", "chunk-gone"), Size: 4,
	})

	pruned, err := PruneUploadJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	store, err := newUploadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, p := range store.PendingUploads() {
		paths[p.Path] = true
	}
	if !paths["/keep.txt"] {
		t.Fatalf("keep.txt was pruned: %v", paths)
	}
	if paths["/gone.txt"] {
		t.Fatalf("gone.txt still pending after prune: %v", paths)
	}
}
