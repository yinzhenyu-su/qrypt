package vfs

import (
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (v *VFS) PendingUploads() []PendingUpload {
	return v.uploads.PendingUploads()
}

func (c *uploadStore) PendingUploads() []PendingUpload {
	c.mu.RLock()
	files := make([]PendingUpload, 0, len(c.pending))
	for _, pending := range c.pending {
		files = append(files, pending)
	}
	c.mu.RUnlock()
	for i := range files {
		files[i].Staging = c.uploadStagingStatus(files[i])
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}
func (c *uploadStore) uploadStagingStatus(p PendingUpload) *UploadStagingStatus {
	status := &UploadStagingStatus{}
	info, err := os.Stat(p.LocalPath)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Exists = true
	status.Size = info.Size()
	status.SizeMatches = status.Size == p.Size
	return status
}
func (c *uploadStore) UploadByPath(path string) (PendingUpload, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	pending, ok := c.pending[path]
	return pending, ok
}
func (c *uploadStore) SaveUpload(p PendingUpload) error {
	p.UpdatedAt = timeutil.Now().UnixNano()
	return c.saveUpload(p)
}
func (c *uploadStore) SaveUploadExact(p PendingUpload) error {
	if p.UpdatedAt == 0 {
		p.UpdatedAt = timeutil.Now().UnixNano()
	}
	return c.saveUpload(p)
}
func (c *uploadStore) saveUpload(p PendingUpload) error {
	c.mu.Lock()
	c.pending[p.Path] = p
	c.mu.Unlock()
	return c.appendJournal(journalEntry{Op: "dirty", PendingUpload: p})
}
func (c *uploadStore) UpdateUploadTransient(p PendingUpload) {
	p.UpdatedAt = timeutil.Now().UnixNano()
	c.mu.Lock()
	c.pending[p.Path] = p
	c.mu.Unlock()
}
func (c *uploadStore) RecordUploadFailure(path string, err error, retryDelay time.Duration) (PendingUpload, bool, error) {
	now := timeutil.Now()
	c.mu.Lock()
	pending, ok := c.pending[path]
	if ok {
		pending.RetryCount++
		if err != nil {
			pending.LastError = err.Error()
		}
		pending.LastAttemptAt = now.UnixNano()
		if retryDelay > 0 {
			pending.NextAttemptAt = now.Add(retryDelay).UnixNano()
		} else {
			pending.NextAttemptAt = 0
		}
		pending.UpdatedAt = now.UnixNano()
		c.pending[path] = pending
	}
	c.mu.Unlock()
	if !ok {
		return PendingUpload{}, false, nil
	}
	return pending, true, c.appendJournal(journalEntry{Op: "dirty", PendingUpload: pending})
}
func (c *uploadStore) RecordUploadReplacementIfUnchanged(p PendingUpload, upload UploadReplacement) (PendingUpload, bool, error) {
	now := timeutil.Now()
	c.mu.Lock()
	pending, ok := c.pending[p.Path]
	if ok && sameUploadRecord(pending, p) {
		pending.ReplaceUpload = &upload
		pending.LastError = ""
		pending.NextAttemptAt = 0
		pending.UpdatedAt = now.UnixNano()
		c.pending[p.Path] = pending
	} else {
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return PendingUpload{}, false, nil
	}
	return pending, true, c.appendJournal(journalEntry{Op: "dirty", PendingUpload: pending})
}
func (c *uploadStore) RecordUploadPermanentFailure(path string, err error) (PendingUpload, bool, error) {
	now := timeutil.Now()
	c.mu.Lock()
	pending, ok := c.pending[path]
	if ok {
		pending.RetryCount++
		if err != nil {
			pending.LastError = err.Error()
		}
		pending.PermanentFail = true
		pending.LastAttemptAt = now.UnixNano()
		pending.NextAttemptAt = 0
		pending.UpdatedAt = now.UnixNano()
		c.pending[path] = pending
	}
	c.mu.Unlock()
	if !ok {
		return PendingUpload{}, false, nil
	}
	return pending, true, c.appendJournal(journalEntry{Op: "dirty", PendingUpload: pending})
}

// RecordUploadFailureIfUnchanged records a failure only if the pending
// file has not changed since it was enqueued. This prevents an old
// frozen generation's upload failure from clobbering a newer generation's state.
func (c *uploadStore) RecordUploadFailureIfUnchanged(p PendingUpload, err error, retryDelay time.Duration) (PendingUpload, bool, error) {
	now := timeutil.Now()
	c.mu.Lock()
	current, ok := c.pending[p.Path]
	if ok && sameUploadRecord(current, p) {
		current.RetryCount++
		if err != nil {
			current.LastError = err.Error()
		}
		current.LastAttemptAt = now.UnixNano()
		if retryDelay > 0 {
			current.NextAttemptAt = now.Add(retryDelay).UnixNano()
		} else {
			current.NextAttemptAt = 0
		}
		current.UpdatedAt = now.UnixNano()
		c.pending[p.Path] = current
	} else {
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return PendingUpload{}, false, nil
	}
	return current, true, c.appendJournal(journalEntry{Op: "dirty", PendingUpload: current})
}

// RecordUploadPermanentFailureIfUnchanged records a permanent failure only
// if the pending file has not changed since it was enqueued.
func (c *uploadStore) RecordUploadPermanentFailureIfUnchanged(p PendingUpload, err error) (PendingUpload, bool, error) {
	now := timeutil.Now()
	c.mu.Lock()
	current, ok := c.pending[p.Path]
	if ok && sameUploadRecord(current, p) {
		current.RetryCount++
		if err != nil {
			current.LastError = err.Error()
		}
		current.PermanentFail = true
		current.LastAttemptAt = now.UnixNano()
		current.NextAttemptAt = 0
		current.UpdatedAt = now.UnixNano()
		c.pending[p.Path] = current
	} else {
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return PendingUpload{}, false, nil
	}
	return current, true, c.appendJournal(journalEntry{Op: "dirty", PendingUpload: current})
}
func (c *uploadStore) RemoveUpload(path string) error {
	c.mu.Lock()
	pending, ok := c.pending[path]
	delete(c.pending, path)
	c.mu.Unlock()
	if !ok {
		return nil
	}
	_ = c.staging.remove(pending.LocalPath)
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if err := c.appendJournalLocked(journalEntry{Op: "clean", PendingUpload: PendingUpload{Path: path}}); err != nil {
		return err
	}
	return c.compactJournalLocked()
}

// removeStagingIfUnreferenced removes a staging file only when no current
// pending points at it. RenameUpload reuses the same staging file under a
// new path, so a bare existence check is not enough.
func (c *uploadStore) removeStagingIfUnreferenced(localPath string) {
	if localPath == "" {
		return
	}
	c.mu.RLock()
	inUse := false
	for _, p := range c.pending {
		if p.LocalPath == localPath {
			inUse = true
			break
		}
	}
	c.mu.RUnlock()
	if inUse {
		return
	}
	if err := c.staging.remove(localPath); err != nil {
		logging.L.Warnf("[CACHE] remove unreferenced staging failed local=%q err=%v", localPath, err)
	}
}

// sweepUnreferencedStaging deletes staging files no pending refers to.
// Generations superseded before a crash are otherwise leaked forever.
func (c *uploadStore) sweepUnreferencedStaging() int {
	c.mu.RLock()
	referenced := make(map[string]struct{}, len(c.pending))
	for _, p := range c.pending {
		referenced[filepath.Clean(p.LocalPath)] = struct{}{}
	}
	c.mu.RUnlock()
	entries, err := os.ReadDir(c.staging.dir)
	if err != nil {
		return 0
	}
	var cleaned int
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".staging") {
			continue
		}
		path := filepath.Join(c.staging.dir, entry.Name())
		if _, ok := referenced[filepath.Clean(path)]; ok {
			continue
		}
		if err := os.Remove(path); err == nil {
			cleaned++
		}
	}
	return cleaned
}
func (c *uploadStore) RemoveUploadsUnder(dir string) error {
	dir = cleanVirtual(dir)
	c.mu.Lock()
	var removed []PendingUpload
	for path, pending := range c.pending {
		if path == dir || isPathUnder(path, dir) {
			delete(c.pending, path)
			removed = append(removed, pending)
		}
	}
	c.mu.Unlock()
	if len(removed) == 0 {
		return nil
	}
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	for _, pending := range removed {
		_ = c.staging.remove(pending.LocalPath)
		if err := c.appendJournalLocked(journalEntry{Op: "clean", PendingUpload: PendingUpload{Path: pending.Path}}); err != nil {
			return err
		}
	}
	return c.compactJournalLocked()
}
func (c *uploadStore) RemoveUploadIfUnchanged(p PendingUpload) (bool, error) {
	c.mu.Lock()
	current, ok := c.pending[p.Path]
	if ok && sameUploadRecord(current, p) {
		delete(c.pending, p.Path)
	} else {
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return false, nil
	}
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if err := c.appendJournalLocked(journalEntry{Op: "clean", PendingUpload: PendingUpload{Path: p.Path}}); err != nil {
		return false, err
	}
	return true, c.compactJournalLocked()
}
func (c *uploadStore) RenameUpload(oldPath string, next PendingUpload) error {
	c.mu.Lock()
	delete(c.pending, oldPath)
	c.pending[next.Path] = next
	c.mu.Unlock()
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if err := c.appendJournalLocked(journalEntry{Op: "clean", PendingUpload: PendingUpload{Path: oldPath}}); err != nil {
		return err
	}
	if err := c.appendJournalLocked(journalEntry{Op: "dirty", PendingUpload: next}); err != nil {
		return err
	}
	return c.compactJournalLocked()
}
func sameUploadRecord(a, b PendingUpload) bool {
	return a.Path == b.Path &&
		a.FID == b.FID &&
		a.ParentID == b.ParentID &&
		a.Name == b.Name &&
		a.LocalPath == b.LocalPath &&
		a.Size == b.Size &&
		a.ModTime == b.ModTime &&
		a.UpdatedAt == b.UpdatedAt &&
		a.RetryCount == b.RetryCount &&
		a.LastError == b.LastError &&
		a.PermanentFail == b.PermanentFail &&
		a.LastAttemptAt == b.LastAttemptAt &&
		a.NextAttemptAt == b.NextAttemptAt &&
		sameUploadReplacement(a.ReplaceUpload, b.ReplaceUpload)
}
func sameUploadReplacement(a, b *UploadReplacement) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID == b.ID &&
		a.ParentID == b.ParentID &&
		a.Name == b.Name &&
		a.Size == b.Size
}
