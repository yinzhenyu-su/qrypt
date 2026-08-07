package vfs

import (
	"bufio"
	"encoding/json"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"os"
	"path/filepath"
)

func (c *uploadStore) journalPath() string {
	return filepath.Join(c.dir, "pending.jsonl")
}
func (c *uploadStore) appendJournal(entry journalEntry) error {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if err := c.appendJournalLocked(entry); err != nil {
		return err
	}
	if c.shouldCompactJournal(0) {
		return c.compactJournalLocked()
	}
	return nil
}
func (c *uploadStore) appendJournalLocked(entry journalEntry) error {
	f, err := os.OpenFile(c.journalPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return nil
}
func (c *uploadStore) loadJournal() (int, error) {
	f, err := os.Open(c.journalPath())
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var entries int
	for scanner.Scan() {
		entries++
		var entry journalEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		switch entry.Op {
		case "dirty":
			if _, err := os.Stat(entry.LocalPath); err == nil {
				c.pending[entry.Path] = entry.PendingUpload
			}
		case "clean":
			delete(c.pending, entry.Path)
		}
	}
	// Rebuild fid->path index after journal replay.
	for _, p := range c.pending {
		if p.FID != "" {
			c.idIndex[p.FID] = p.Path
		}
	}
	return entries, scanner.Err()
}
func (c *uploadStore) shouldCompactJournal(entries int) bool {
	info, err := os.Stat(c.journalPath())
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return false
	}
	if info.Size() >= journalCompactMaxBytes {
		return true
	}
	if entries == 0 {
		entries = countJournalEntries(c.journalPath())
	}
	c.mu.RLock()
	pendingCount := len(c.pending)
	c.mu.RUnlock()
	return entries >= journalCompactMaxEntries && entries > pendingCount+32
}
func countJournalEntries(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var entries int
	for scanner.Scan() {
		entries++
	}
	if err := scanner.Err(); err != nil {
		logging.L.Warnf("[CACHE] count pending journal entries failed: %v", err)
	}
	return entries
}
func (c *uploadStore) compactJournal() error {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	return c.compactJournalLocked()
}
func (c *uploadStore) compactJournalLocked() error {
	tmp := c.journalPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	c.mu.RLock()
	for _, p := range c.pending {
		data, err := json.Marshal(journalEntry{Op: "dirty", PendingUpload: p})
		if err != nil {
			c.mu.RUnlock()
			f.Close()
			return err
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			c.mu.RUnlock()
			f.Close()
			return err
		}
	}
	c.mu.RUnlock()
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.journalPath()); err != nil {
		return err
	}
	_ = syncParentDir(c.journalPath())
	return nil
}
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
func syncParentDir(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// --- upload_journal_ops.go ---

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

// PruneUploadJournal rewrites the journal to only the current pending set:
// entries are replayed by path (dirty overrides, clean removes) and then
// dirty entries whose staging file is gone or size-mismatched are dropped,
// since their offline data no longer exists. Terminal clean markers and
// corrupt lines are dropped. It returns the number of dropped entries.
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

	// Replay the journal into the final per-path state, exactly like
	// loadJournal: a later clean supersedes an earlier dirty, so a finished
	// upload never comes back just because its old dirty line was kept.
	pending := map[string]journalEntry{}
	pruned := 0
	changed := false // a clean marker or unparsable line requires a rewrite
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var entry journalEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			pruned++ // tolerate and drop corrupt lines, like loadJournal does
			changed = true
			continue
		}
		switch entry.Op {
		case "dirty":
			pending[entry.Path] = entry
		case "clean":
			delete(pending, entry.Path)
			changed = true
		default:
			pruned++
			changed = true
		}
	}
	if err := scanner.Err(); err != nil {
		return pruned, err
	}

	// Drop entries whose staging data is gone; what remains is the kept set.
	var kept []journalEntry
	for _, entry := range pending {
		info, statErr := os.Stat(entry.LocalPath)
		if statErr != nil || info.Size() != entry.Size {
			pruned++
			continue
		}
		kept = append(kept, entry)
	}
	if pruned == 0 && !changed {
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
