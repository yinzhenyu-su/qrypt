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
