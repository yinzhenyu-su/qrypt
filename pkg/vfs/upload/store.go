package upload

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

type UploadStagingStatus = vfstypes.UploadStagingStatus

func cleanVirtual(path string) string   { return vfstypes.CleanVirtualPath(path) }
func isPathUnder(path, dir string) bool { return vfstypes.IsPathUnder(path, dir) }

const (
	journalCompactMaxBytes   = 512 << 10
	journalCompactMaxEntries = 1024
)

// NewPendingStore creates the persistent pending-upload store and loads any
// journal left by a previous run.
func NewPendingStore(dir string) (*PendingStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	staging, err := newStagingStore(filepath.Join(dir, "staging"))
	if err != nil {
		return nil, err
	}
	if cleaned := staging.cleanupUploadTemps(); cleaned > 0 {
		logging.L.Infof("[CACHE] cleaned %d orphaned staging upload files", cleaned)
	}
	store := &PendingStore{
		dir:       dir,
		staging:   staging,
		pending:   map[string]PendingUpload{},
		idIndex:   map[string]string{},
		journalMu: sync.Mutex{},
	}
	entries, err := store.loadJournal()
	if err != nil {
		return nil, err
	}
	if store.shouldCompactJournal(entries) {
		if err := store.compactJournal(); err != nil {
			logging.L.Warnf("[CACHE] compact pending journal failed: %v", err)
		}
	}
	if cleaned := store.sweepUnreferencedStaging(); cleaned > 0 {
		logging.L.Infof("[CACHE] cleaned %d unreferenced staging files", cleaned)
	}
	return store, nil
}

// DebugJournal is a snapshot of the pending-upload journal file.
type DebugJournal struct {
	Path               string             `json:"path"`
	Exists             bool               `json:"exists"`
	Bytes              int64              `json:"bytes,omitempty"`
	Entries            int                `json:"entries,omitempty"`
	InvalidEntries     int                `json:"invalid_entries,omitempty"`
	PendingCount       int                `json:"pending_count"`
	UniquePaths        int                `json:"unique_paths,omitempty"`
	DuplicateEntries   int                `json:"duplicate_entries,omitempty"`
	CompactRecommended bool               `json:"compact_recommended"`
	LargestPaths       []DebugJournalPath `json:"largest_paths,omitempty"`
	Error              string             `json:"error,omitempty"`
}

// DebugJournalPath is per-path journal statistics.
type DebugJournalPath struct {
	Path             string `json:"path"`
	Entries          int    `json:"entries"`
	LatestSize       int64  `json:"latest_size,omitempty"`
	StagingSize      int64  `json:"staging_size,omitempty"`
	SizeMatches      bool   `json:"size_matches"`
	StagingExists    bool   `json:"staging_exists"`
	LastJournalOp    string `json:"last_journal_op,omitempty"`
	LastJournalLine  int    `json:"last_journal_line,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	DuplicateEntries int    `json:"duplicate_entries,omitempty"`
}

// ---- store types ----

type PendingStore struct {
	dir     string
	staging *stagingStore

	mu        sync.RWMutex
	journalMu sync.Mutex
	// txMu serializes the full snapshot -> journal -> memory write
	// transactions, so a concurrent Save/Remove can never interleave
	// between a snapshot and its memory apply.
	txMu    sync.Mutex
	pending map[string]PendingUpload
	idIndex map[string]string // fid -> path for O(1) PendingByID

	// journalFail, when non-nil, injects a failure into journal appends;
	// compactFail injects one into compactions (test-only hooks for
	// write-ahead fault injection).
	journalFail func() error
	compactFail func() error
}

type JournalEntry struct {
	Op      string   `json:"op"`
	OldPath string   `json:"old_path,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	PendingUpload
}

type stagingStore struct {
	dir   string
	pages sync.Map
}

type page struct {
	mu        sync.Mutex
	fid       string
	buf       []byte
	dirty     bool
	maxOffset int64
	timer     *time.Timer
	flush     func(string, []byte) error
}

func newStagingStore(dir string) (*stagingStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &stagingStore{dir: dir}, nil
}

func (s *stagingStore) cleanupUploadTemps() int {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0
	}
	var cleaned int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.Contains(name, ".staging.upload-") {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err == nil {
			cleaned++
		}
	}
	return cleaned
}

func (s *stagingStore) path(fid string) string {
	return filepath.Join(s.dir, fid+".staging")
}

func (s *stagingStore) create(fid string) (string, error) {
	path := s.path(fid)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}

func (s *stagingStore) writeAt(path string, data []byte, off int64) (int, error) {
	if err := s.flush(path); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.WriteAt(data, off)
}

func (s *stagingStore) size(path string) (int64, error) {
	if err := s.flush(path); err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s *stagingStore) truncate(path string, size int64) error {
	if err := s.flush(path); err != nil {
		return err
	}
	s.pages.Delete(fidFromStagingPath(path))
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := s.create(fidFromStagingPath(path)); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := os.Truncate(path, size); err != nil {
		return err
	}
	return s.sync(path)
}

func (s *stagingStore) remove(path string) error {
	s.pages.Delete(fidFromStagingPath(path))
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *stagingStore) flush(path string) error {
	if p, ok := s.pages.Load(fidFromStagingPath(path)); ok {
		return p.(*page).flushNow()
	}
	return nil
}

func (s *stagingStore) sync(path string) error {
	if err := s.flush(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (p *page) flushNow() error {
	p.mu.Lock()
	if !p.dirty {
		p.mu.Unlock()
		return nil
	}
	data := make([]byte, p.maxOffset)
	copy(data, p.buf[:p.maxOffset])
	p.dirty = false
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.mu.Unlock()
	return p.flush(p.fid, data)
}

func fidFromStagingPath(path string) string {
	base := filepath.Base(path)
	if filepath.Ext(base) == ".staging" {
		return base[:len(base)-len(".staging")]
	}
	return base
}

// ---- uploadStore methods ----

// StagingDir returns the directory holding staging files.
func (c *PendingStore) StagingDir() string { return c.staging.dir }

func (c *PendingStore) PendingUploads() []PendingUpload {
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
func (c *PendingStore) uploadStagingStatus(p PendingUpload) *UploadStagingStatus {
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
func (c *PendingStore) UploadByPath(path string) (PendingUpload, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	pending, ok := c.pending[path]
	return pending, ok
}
func (c *PendingStore) SaveUpload(p PendingUpload) error {
	p.UpdatedAt = util.Now().UnixNano()
	return c.saveUpload(p)
}
func (c *PendingStore) SaveUploadExact(p PendingUpload) error {
	if p.UpdatedAt == 0 {
		p.UpdatedAt = util.Now().UnixNano()
	}
	return c.saveUpload(p)
}
func (c *PendingStore) saveUpload(p PendingUpload) error {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	// Journal commit FIRST: memory is only touched after the update is
	// durable, so an append failure leaves no record behind.
	if err := c.appendJournal(JournalEntry{Op: "dirty", PendingUpload: p}); err != nil {
		return err
	}
	c.mu.Lock()
	c.pending[p.Path] = p
	if p.FID != "" {
		c.idIndex[p.FID] = p.Path
	}
	c.mu.Unlock()
	return nil
}
func (c *PendingStore) UpdateUploadTransient(p PendingUpload) {
	p.UpdatedAt = util.Now().UnixNano()
	c.mu.Lock()
	c.pending[p.Path] = p
	if p.FID != "" {
		c.idIndex[p.FID] = p.Path
	}
	c.mu.Unlock()
}
func (c *PendingStore) RecordUploadFailure(path string, err error, retryDelay time.Duration) (PendingUpload, bool, error) {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	now := util.Now()
	c.mu.RLock()
	pending, ok := c.pending[path]
	if !ok {
		c.mu.RUnlock()
		return PendingUpload{}, false, nil
	}
	next := pending
	next.RetryCount++
	if err != nil {
		next.LastError = err.Error()
	}
	next.LastAttemptAt = now.UnixNano()
	if retryDelay > 0 {
		next.NextAttemptAt = now.Add(retryDelay).UnixNano()
	} else {
		next.NextAttemptAt = 0
	}
	next.UpdatedAt = now.UnixNano()
	c.mu.RUnlock()
	// Journal commit FIRST: on failure the old state stays in memory.
	if err := c.appendJournal(JournalEntry{Op: "dirty", PendingUpload: next}); err != nil {
		return pending, true, err
	}
	c.mu.Lock()
	c.pending[path] = next
	c.mu.Unlock()
	return next, true, nil
}
func (c *PendingStore) RecordUploadReplacementIfUnchanged(p PendingUpload, upload UploadReplacement) (PendingUpload, bool, error) {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	now := util.Now()
	c.mu.RLock()
	pending, ok := c.pending[p.Path]
	if !ok || !sameUploadRecord(pending, p) {
		c.mu.RUnlock()
		return PendingUpload{}, false, nil
	}
	next := pending
	next.ReplaceUpload = &upload
	next.LastError = ""
	next.NextAttemptAt = 0
	next.UpdatedAt = now.UnixNano()
	c.mu.RUnlock()
	if err := c.appendJournal(JournalEntry{Op: "dirty", PendingUpload: next}); err != nil {
		return pending, true, err
	}
	c.mu.Lock()
	c.pending[p.Path] = next
	c.mu.Unlock()
	return next, true, nil
}
func (c *PendingStore) RecordUploadPermanentFailure(path string, err error) (PendingUpload, bool, error) {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	now := util.Now()
	c.mu.RLock()
	pending, ok := c.pending[path]
	if !ok {
		c.mu.RUnlock()
		return PendingUpload{}, false, nil
	}
	next := pending
	next.RetryCount++
	if err != nil {
		next.LastError = err.Error()
	}
	next.PermanentFail = true
	next.LastAttemptAt = now.UnixNano()
	next.NextAttemptAt = 0
	next.UpdatedAt = now.UnixNano()
	c.mu.RUnlock()
	if err := c.appendJournal(JournalEntry{Op: "dirty", PendingUpload: next}); err != nil {
		return pending, true, err
	}
	c.mu.Lock()
	c.pending[path] = next
	c.mu.Unlock()
	return next, true, nil
}

// RecordUploadFailureIfUnchanged records a failure only if the pending
// file has not changed since it was enqueued. This prevents an old
// frozen generation's upload failure from clobbering a newer generation's state.
func (c *PendingStore) RecordUploadFailureIfUnchanged(p PendingUpload, err error, retryDelay time.Duration) (PendingUpload, bool, error) {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	now := util.Now()
	c.mu.RLock()
	current, ok := c.pending[p.Path]
	if !ok || !sameUploadRecord(current, p) {
		c.mu.RUnlock()
		return PendingUpload{}, false, nil
	}
	next := current
	next.RetryCount++
	if err != nil {
		next.LastError = err.Error()
	}
	next.LastAttemptAt = now.UnixNano()
	if retryDelay > 0 {
		next.NextAttemptAt = now.Add(retryDelay).UnixNano()
	} else {
		next.NextAttemptAt = 0
	}
	next.UpdatedAt = now.UnixNano()
	c.mu.RUnlock()
	if err := c.appendJournal(JournalEntry{Op: "dirty", PendingUpload: next}); err != nil {
		return current, true, err
	}
	c.mu.Lock()
	c.pending[p.Path] = next
	c.mu.Unlock()
	return next, true, nil
}

// RecordUploadPermanentFailureIfUnchanged records a permanent failure only
// if the pending file has not changed since it was enqueued.
func (c *PendingStore) RecordUploadPermanentFailureIfUnchanged(p PendingUpload, err error) (PendingUpload, bool, error) {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	now := util.Now()
	c.mu.RLock()
	current, ok := c.pending[p.Path]
	if !ok || !sameUploadRecord(current, p) {
		c.mu.RUnlock()
		return PendingUpload{}, false, nil
	}
	next := current
	next.RetryCount++
	if err != nil {
		next.LastError = err.Error()
	}
	next.PermanentFail = true
	next.LastAttemptAt = now.UnixNano()
	next.NextAttemptAt = 0
	next.UpdatedAt = now.UnixNano()
	c.mu.RUnlock()
	if err := c.appendJournal(JournalEntry{Op: "dirty", PendingUpload: next}); err != nil {
		return current, true, err
	}
	c.mu.Lock()
	c.pending[p.Path] = next
	c.mu.Unlock()
	return next, true, nil
}
func (c *PendingStore) RemoveUpload(path string) error {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	c.mu.RLock()
	pending, ok := c.pending[path]
	c.mu.RUnlock()
	if !ok {
		return nil
	}
	// Write-ahead: persist the clean intent BEFORE mutating memory, so a
	// journal failure leaves pending/idIndex/staging untouched.
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if err := c.appendJournalLocked(JournalEntry{Op: "clean", PendingUpload: PendingUpload{Path: path}}); err != nil {
		return err
	}
	// Apply in memory; the staging file drops after the record is gone.
	c.mu.Lock()
	delete(c.pending, path)
	if pending.FID != "" {
		delete(c.idIndex, pending.FID)
	}
	if pending.LocalPath != "" {
		if err := c.staging.remove(pending.LocalPath); err != nil {
			logging.L.Warnf("[CACHE] remove staging failed local=%q err=%v", pending.LocalPath, err)
		}
	}
	c.mu.Unlock()
	// Compact is maintenance: the clean intent is already durable, so a
	// compact failure is logged, not reported as an uncommitted delete.
	if err := c.compactJournalLocked(); err != nil {
		logging.L.Warnf("[CACHE] journal compact failed after remove path=%q err=%v", path, err)
	}
	return nil
}

// removeStagingIfUnreferenced removes a staging file only when no current
// pending points at it. RenameUpload reuses the same staging file under a
// new path, so a bare existence check is not enough.
func (c *PendingStore) RemoveStagingIfUnreferenced(localPath string) {
	if localPath == "" {
		return
	}
	// Serialize the reference check and removal with durable pending
	// mutations. In particular, RenameUpload must not make this staging
	// current after the check but before it is removed.
	c.txMu.Lock()
	defer c.txMu.Unlock()
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
// It runs only while constructing the store, before it serves traffic, so
// there is no create-to-register window to race.
func (c *PendingStore) sweepUnreferencedStaging() int {
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
func (c *PendingStore) RemoveUploadsUnder(dir string) error {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	dir = cleanVirtual(dir)
	c.mu.RLock()
	var removed []PendingUpload
	for path, pending := range c.pending {
		if path == dir || isPathUnder(path, dir) {
			removed = append(removed, pending)
		}
	}
	c.mu.RUnlock()
	if len(removed) == 0 {
		return nil
	}
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	// Write the whole batch as ONE clean_batch entry: on failure, memory
	// and staging stay untouched, and replay applies the batch atomically.
	paths := make([]string, 0, len(removed))
	for _, pending := range removed {
		paths = append(paths, pending.Path)
	}
	if err := c.appendCleanBatchLocked(paths); err != nil {
		return err
	}
	c.mu.Lock()
	for _, pending := range removed {
		delete(c.pending, pending.Path)
		if pending.FID != "" {
			delete(c.idIndex, pending.FID)
		}
	}
	c.mu.Unlock()
	for _, pending := range removed {
		if pending.LocalPath != "" {
			if err := c.staging.remove(pending.LocalPath); err != nil {
				logging.L.Warnf("[CACHE] remove staging failed local=%q err=%v", pending.LocalPath, err)
			}
		}
	}
	if err := c.compactJournalLocked(); err != nil {
		logging.L.Warnf("[CACHE] journal compact failed after subtree remove dir=%q err=%v", dir, err)
	}
	return nil
}
func (c *PendingStore) RemoveUploadIfUnchanged(p PendingUpload) (bool, error) {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	c.mu.RLock()
	current, ok := c.pending[p.Path]
	c.mu.RUnlock()
	if !ok || !sameUploadRecord(current, p) {
		return false, nil
	}
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	// Write-ahead before any memory mutation.
	if err := c.appendJournalLocked(JournalEntry{Op: "clean", PendingUpload: PendingUpload{Path: p.Path}}); err != nil {
		return false, err
	}
	c.mu.Lock()
	current, ok = c.pending[p.Path]
	if ok && sameUploadRecord(current, p) {
		delete(c.pending, p.Path)
		if current.FID != "" {
			delete(c.idIndex, current.FID)
		}
		// Drop the staging file (unless another pending still references
		// it, as rename/replace can reuse one) so readers never observe a
		// pending gone while its staging lingers.
		c.removeStagingLocked(p.LocalPath)
	} else {
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		// The record moved while appending; the clean intent is harmless.
		return false, nil
	}
	// Compact is maintenance; the clean intent is already durable.
	if err := c.compactJournalLocked(); err != nil {
		logging.L.Warnf("[CACHE] journal compact failed after remove path=%q err=%v", p.Path, err)
	}
	return true, nil
}

// removeStagingLocked removes a staging file unless another pending still
// references it. Caller must hold c.mu.
func (c *PendingStore) removeStagingLocked(localPath string) {
	if localPath == "" {
		return
	}
	for _, p := range c.pending {
		if p.LocalPath == localPath {
			return
		}
	}
	if err := c.staging.remove(localPath); err != nil {
		logging.L.Warnf("[CACHE] remove unreferenced staging failed local=%q err=%v", localPath, err)
	}
}

// PendingByID returns the pending upload with the given file ID, or false
// when none matches. The lookup is O(1) via the idIndex map.
func (c *PendingStore) PendingByID(id string) (PendingUpload, bool) {
	c.mu.RLock()
	path, ok := c.idIndex[id]
	c.mu.RUnlock()
	if !ok {
		return PendingUpload{}, false
	}
	c.mu.RLock()
	pending, ok := c.pending[path]
	c.mu.RUnlock()
	if !ok || pending.FID != id {
		return PendingUpload{}, false
	}
	return pending, true
}

func (c *PendingStore) RenameUpload(oldPath string, next PendingUpload) error {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	// Journal commit FIRST, as ONE transactional "rename" entry: replay
	// deletes the old path and sets the new path atomically, so a
	// crash between the old clean and the new dirty intents can never
	// resurrect the old name.
	if err := c.appendJournal(JournalEntry{Op: "rename", OldPath: oldPath, PendingUpload: next}); err != nil {
		return err
	}
	c.mu.Lock()
	if old, ok := c.pending[oldPath]; ok && old.FID != "" {
		delete(c.idIndex, old.FID)
	}
	delete(c.pending, oldPath)
	c.pending[next.Path] = next
	if next.FID != "" {
		c.idIndex[next.FID] = next.Path
	}
	c.mu.Unlock()
	return nil
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

// ---- journal methods ----

func (c *PendingStore) journalPath() string {
	return filepath.Join(c.dir, "pending.jsonl")
}
func (c *PendingStore) appendJournal(entry JournalEntry) error {
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

// appendCleanBatchLocked persists one clean_batch entry covering all
// paths, so a directory removal's clean intents are durable in a single
// append/fsync (no partial batch on replay).
func (c *PendingStore) appendCleanBatchLocked(paths []string) error {
	return c.appendJournalLocked(JournalEntry{Op: "clean_batch", Paths: paths})
}

func (c *PendingStore) appendJournalLocked(entry JournalEntry) error {
	if c.journalFail != nil {
		if err := c.journalFail(); err != nil {
			return err
		}
	}
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
func (c *PendingStore) loadJournal() (int, error) {
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
		var entry JournalEntry
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
		case "clean_batch":
			for _, p := range entry.Paths {
				delete(c.pending, p)
			}
		case "rename":
			delete(c.pending, entry.OldPath)
			c.pending[entry.Path] = entry.PendingUpload
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
func (c *PendingStore) shouldCompactJournal(entries int) bool {
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
func (c *PendingStore) compactJournal() error {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	return c.compactJournalLocked()
}
func (c *PendingStore) compactJournalLocked() error {
	if c.compactFail != nil {
		if err := c.compactFail(); err != nil {
			return err
		}
	}
	tmp := c.journalPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	c.mu.RLock()
	for _, p := range c.pending {
		data, err := json.Marshal(JournalEntry{Op: "dirty", PendingUpload: p})
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
	store, err := NewPendingStore(dir)
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
	pending := map[string]JournalEntry{}
	pruned := 0
	changed := false // a clean marker or unparsable line requires a rewrite
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var entry JournalEntry
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
	var kept []JournalEntry
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

// ---- store adapter ----

type StoreAdapter struct {
	store *PendingStore
}

func NewStoreAdapter(store *PendingStore) StoreAdapter {
	return StoreAdapter{store: store}
}

func (a StoreAdapter) UploadByPath(path string) (PendingUpload, bool) {
	return a.store.UploadByPath(path)
}

func (a StoreAdapter) RemoveStagingIfUnreferenced(localPath string) {
	a.store.RemoveStagingIfUnreferenced(localPath)
}

func (a StoreAdapter) RecordPermanentFailureIfUnchanged(pending PendingUpload, err error) (PendingUpload, bool, error) {
	return a.store.RecordUploadPermanentFailureIfUnchanged(pending, err)
}

func (a StoreAdapter) RecordFailureIfUnchanged(pending PendingUpload, err error, retryDelay time.Duration) (PendingUpload, bool, error) {
	return a.store.RecordUploadFailureIfUnchanged(pending, err, retryDelay)
}

func (a StoreAdapter) RecordReplacementIfUnchanged(pending PendingUpload, replacement UploadReplacement) (PendingUpload, bool, error) {
	return a.store.RecordUploadReplacementIfUnchanged(pending, replacement)
}

func (a StoreAdapter) RemoveIfUnchanged(pending PendingUpload) (bool, error) {
	return a.store.RemoveUploadIfUnchanged(pending)
}

func (a StoreAdapter) RemoveStaging(localPath string) error {
	return a.store.staging.remove(localPath)
}

// ---- debug journal ----

func (c *PendingStore) DebugJournal() *DebugJournal {
	path := c.journalPath()
	journal := &DebugJournal{Path: path}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		c.mu.RLock()
		journal.PendingCount = len(c.pending)
		c.mu.RUnlock()
		return journal
	}
	if err != nil {
		journal.Error = err.Error()
		return journal
	}
	journal.Exists = true
	journal.Bytes = info.Size()

	f, err := os.Open(path)
	if err != nil {
		journal.Error = err.Error()
		return journal
	}
	defer f.Close()

	type pathState struct {
		item DebugJournalPath
	}
	paths := map[string]*pathState{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line := 0
	validEntries := 0
	for scanner.Scan() {
		line++
		journal.Entries++
		var entry JournalEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			journal.InvalidEntries++
			continue
		}
		if entry.Path == "" {
			journal.InvalidEntries++
			continue
		}
		state := paths[entry.Path]
		if state == nil {
			state = &pathState{item: DebugJournalPath{Path: entry.Path}}
			paths[entry.Path] = state
		}
		validEntries++
		state.item.Entries++
		state.item.LastJournalOp = entry.Op
		state.item.LastJournalLine = line
		if entry.Op == "dirty" {
			state.item.LatestSize = entry.Size
			state.item.LastError = entry.LastError
		}
	}
	if err := scanner.Err(); err != nil {
		journal.Error = err.Error()
	}

	c.mu.RLock()
	journal.PendingCount = len(c.pending)
	pending := make(map[string]PendingUpload, len(c.pending))
	for path, item := range c.pending {
		pending[path] = item
	}
	c.mu.RUnlock()

	journal.UniquePaths = len(paths)
	if validEntries > journal.UniquePaths {
		journal.DuplicateEntries = validEntries - journal.UniquePaths
	}
	journal.CompactRecommended = c.shouldCompactJournal(journal.Entries)

	for path, state := range paths {
		if state.item.Entries > 1 {
			state.item.DuplicateEntries = state.item.Entries - 1
		}
		if p, ok := pending[path]; ok {
			state.item.LatestSize = p.Size
			state.item.LastError = p.LastError
			if info, err := os.Stat(p.LocalPath); err == nil {
				state.item.StagingExists = true
				state.item.StagingSize = info.Size()
				state.item.SizeMatches = info.Size() == p.Size
			}
		}
		journal.LargestPaths = append(journal.LargestPaths, state.item)
	}
	sort.Slice(journal.LargestPaths, func(i, j int) bool {
		if journal.LargestPaths[i].Entries == journal.LargestPaths[j].Entries {
			return journal.LargestPaths[i].Path < journal.LargestPaths[j].Path
		}
		return journal.LargestPaths[i].Entries > journal.LargestPaths[j].Entries
	})
	if len(journal.LargestPaths) > 10 {
		journal.LargestPaths = journal.LargestPaths[:10]
	}
	return journal
}

// --- staging passthrough ---

func (c *PendingStore) CreateStaging(fid string) (string, error) { return c.staging.create(fid) }
func (c *PendingStore) WriteStagingAt(path string, data []byte, off int64) (int, error) {
	return c.staging.writeAt(path, data, off)
}
func (c *PendingStore) FlushStaging(path string) error         { return c.staging.flush(path) }
func (c *PendingStore) SyncStaging(path string) error          { return c.staging.sync(path) }
func (c *PendingStore) StagingSize(path string) (int64, error) { return c.staging.size(path) }
func (c *PendingStore) TruncateStaging(path string, size int64) error {
	return c.staging.truncate(path, size)
}
func (c *PendingStore) RemoveStaging(path string) error { return c.staging.remove(path) }
func (c *PendingStore) StagingPath(fid string) string   { return c.staging.path(fid) }
