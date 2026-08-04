package vfs

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
)

// Offline-upload journal maintenance, driven by the CLI. These operate on a
// journal directory without opening a driver, so they work while the mount
// is offline.

// ReplayUploadJournal resets failure state (permanent failure, last error,
// retry bookkeeping) on every failed pending upload so the next mount retries
// them. It returns the reset entries and writes the reset state back to the
// journal. Entries whose staging file is gone are left alone (prune those
// with PruneUploadJournal first).
func ReplayUploadJournal(dir string) ([]PendingUpload, error) {
	store, err := newUploadStore(dir)
	if err != nil {
		return nil, err
	}
	var replayed []PendingUpload
	for _, pending := range store.PendingUploads() {
		if !pending.PermanentFail && pending.LastError == "" {
			continue
		}
		reset := pending
		reset.Staging = nil // transient runtime diagnostics, not journal state
		reset.PermanentFail = false
		reset.LastError = ""
		reset.RetryCount = 0
		reset.LastAttemptAt = 0
		reset.NextAttemptAt = 0
		if err := store.SaveUploadExact(reset); err != nil {
			return replayed, err
		}
		replayed = append(replayed, reset)
	}
	// Compact so the journal reflects only the current state (the appended
	// resets supersede the old failed entries).
	if len(replayed) > 0 {
		if err := store.compactJournal(); err != nil {
			return replayed, err
		}
	}
	return replayed, nil
}

// PruneUploadJournal rewrites the journal keeping only dirty entries whose
// staging file still exists and matches the recorded size (offline data is
// intact). Terminal clean markers and entries whose data is gone are dropped.
// It returns the number of dropped entries.
func PruneUploadJournal(dir string) (int, error) {
	journalPath := filepath.Join(dir, "pending.jsonl")
	file, err := os.Open(journalPath)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer file.Close()

	var kept []journalEntry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	pruned := 0
	for scanner.Scan() {
		var entry journalEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			pruned++ // tolerate and drop corrupt lines, like loadJournal does
			continue
		}
		switch entry.Op {
		case "dirty":
			info, statErr := os.Stat(entry.LocalPath)
			if statErr != nil || info.Size() != entry.Size {
				pruned++
				continue
			}
			kept = append(kept, entry)
		default:
			pruned++
		}
	}
	if err := scanner.Err(); err != nil {
		return pruned, err
	}
	if pruned == 0 {
		return 0, nil
	}

	tmp := journalPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return pruned, err
	}
	for _, entry := range kept {
		data, err := json.Marshal(entry)
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return pruned, err
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return pruned, err
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return pruned, err
	}
	if err := f.Close(); err != nil {
		return pruned, err
	}
	if err := os.Rename(tmp, journalPath); err != nil {
		return pruned, err
	}
	_ = syncParentDir(journalPath)
	return pruned, nil
}
