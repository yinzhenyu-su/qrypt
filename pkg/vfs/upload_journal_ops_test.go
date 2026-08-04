package vfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// TestPruneUploadJournalRespectsCleanMarkers guards against resurrecting a
// finished upload: the replay must apply dirty/clean by path before pruning,
// so an old dirty line superseded by a later clean never comes back.
func TestPruneUploadJournalRespectsCleanMarkers(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "pending.jsonl")
	doneStaging := filepath.Join(dir, "staging", "chunk-done")
	if err := os.MkdirAll(filepath.Dir(doneStaging), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doneStaging, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	keepStaging := filepath.Join(dir, "staging", "chunk-keep")
	if err := os.WriteFile(keepStaging, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// /done.txt was uploaded and then cleaned; its staging still exists so a
	// naive per-line prune would keep the old dirty line and re-upload it.
	writeJournalEntry(t, journal, "dirty", PendingUpload{
		Path: "/done.txt", FID: "f-done", LocalPath: doneStaging, Size: 4,
	})
	writeJournalEntry(t, journal, "clean", PendingUpload{Path: "/done.txt"})
	// /keep.txt is still pending and valid.
	writeJournalEntry(t, journal, "dirty", PendingUpload{
		Path: "/keep.txt", FID: "f-keep", LocalPath: keepStaging, Size: 4,
	})

	pruned, err := PruneUploadJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 0 {
		t.Fatalf("pruned = %d, want 0 (clean marker removal is not a prune)", pruned)
	}
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, "/done.txt") {
		t.Fatalf("journal resurrected a cleaned upload:\n%s", content)
	}
	if !strings.Contains(content, "/keep.txt") {
		t.Fatalf("journal lost a live entry:\n%s", content)
	}

	// A reload must not see /done.txt as pending either.
	store, err := newUploadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range store.PendingUploads() {
		if p.Path == "/done.txt" {
			t.Fatalf("pruned journal still loads /done.txt as pending")
		}
	}
}
