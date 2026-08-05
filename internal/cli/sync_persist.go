package cli

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

// syncSessionTTL is how long an unfinished session may sit idle before the
// next sync run reaps it. Sessions are transient resume state, never user
// data, so an orphan is always safe to drop (a plain re-run is idempotent).
const syncSessionTTL = 7 * 24 * time.Hour

// syncSessionFlags records the flags a plan was generated with so a resumed
// run keeps the same semantics without requiring the caller to repeat them.
type syncSessionFlags struct {
	Delete   bool   `json:"delete"`
	Hash     bool   `json:"hash"`
	Conflict string `json:"conflict"`
}

// syncSessionPlan is the immutable plan persisted on disk. Ops are never
// rewritten in place: progress is appended to state.jsonl instead.
type syncSessionPlan struct {
	Version     int              `json:"version"`
	Source      string           `json:"source"`
	Destination string           `json:"destination"`
	Flags       syncSessionFlags `json:"flags"`
	Created     time.Time        `json:"created"`
	Ops         []syncPlanEntry  `json:"ops"`
}

// syncStateEntry records one finished plan op (append-only journal).
type syncStateEntry struct {
	Op     string     `json:"op"` // "done"
	Path   string     `json:"path"`
	Action syncAction `json:"action"`
	OK     bool       `json:"ok"`
	Error  string     `json:"error,omitempty"`
}

// syncPersist owns the on-disk session for one SOURCE→DESTINATION pair:
//
//	~/.qrypt/qrypt-sync/<key>/
//	├── plan.json    # immutable plan, written once via atomic rename
//	├── state.jsonl  # append-only progress journal, one op per line
//	└── .lock        # flock guard against concurrent runs
//
// The directory is removed when no transfer ops remain unfinished, and
// reaped by TTL otherwise, so disk usage stays proportional to the number
// of live interrupted sessions.
type syncPersist struct {
	mu   sync.Mutex
	dir  string
	plan syncSessionPlan
	done map[string]bool // syncStateKey → finished OK
	lock *os.File
}

// syncPersistRoot returns the directory that holds all sync sessions. The
// QRYPT_SYNC_DIR override lets tests isolate sessions from the real config.
func syncPersistRoot() string {
	if dir := os.Getenv("QRYPT_SYNC_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(osutil.ExpandHome("~/.qrypt"), "qrypt-sync")
}

// syncTargetDescriptor canonicalizes one sync side so the session key is
// stable across equivalent invocations (trailing slashes, relative paths).
func syncTargetDescriptor(t checkTarget) string {
	if t.kind == targetLocal {
		abs, err := filepath.Abs(t.localPath)
		if err != nil {
			abs = t.localPath
		}
		return "local:" + abs
	}
	return "vfs:" + t.mountName + ":" + pathpkg.Clean(t.vfsPath)
}

// syncSessionKey maps a source/destination pair to its session directory
// name. Deterministic: the same pair always resumes the same session.
func syncSessionKey(source, destination checkTarget) string {
	desc := syncTargetDescriptor(source) + "->" + syncTargetDescriptor(destination)
	sum := sha256.Sum256([]byte(desc))
	return hex.EncodeToString(sum[:8])
}

func syncStateKey(path string, action syncAction) string {
	return path + "|" + string(action)
}

// newSyncSession creates or resets the session for a fresh run and takes an
// exclusive lock so two processes cannot race the same pair.
func newSyncSession(source, destination checkTarget, flags syncSessionFlags, ops []syncPlanEntry) (*syncPersist, error) {
	dir := filepath.Join(syncPersistRoot(), syncSessionKey(source, destination))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create sync session: %w", err)
	}
	lock, err := lockSyncSession(dir)
	if err != nil {
		return nil, err
	}
	p := &syncPersist{
		dir: dir,
		plan: syncSessionPlan{
			Version:     1,
			Source:      syncTargetDescriptor(source),
			Destination: syncTargetDescriptor(destination),
			Flags:       flags,
			Created:     time.Now(),
			Ops:         ops,
		},
		done: map[string]bool{},
		lock: lock,
	}
	if err := p.writePlanLocked(); err != nil {
		lock.Close()
		return nil, err
	}
	// A fresh run starts from a clean progress journal; a previous
	// interrupted run of the same pair is superseded by overwrite.
	if err := p.resetState(); err != nil {
		lock.Close()
		return nil, err
	}
	return p, nil
}

// loadSyncSession opens an existing session for --resume. It returns
// found=false when no session exists for this pair.
func loadSyncSession(source, destination checkTarget) (*syncPersist, bool, error) {
	dir := filepath.Join(syncPersistRoot(), syncSessionKey(source, destination))
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	lock, err := lockSyncSession(dir)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		lock.Close()
		return nil, false, fmt.Errorf("load sync plan: %w", err)
	}
	var plan syncSessionPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		lock.Close()
		return nil, false, fmt.Errorf("parse sync plan: %w", err)
	}
	if plan.Source != syncTargetDescriptor(source) || plan.Destination != syncTargetDescriptor(destination) {
		lock.Close()
		return nil, false, fmt.Errorf("sync session belongs to %s -> %s, not %s -> %s",
			plan.Source, plan.Destination, syncTargetDescriptor(source), syncTargetDescriptor(destination))
	}
	p := &syncPersist{dir: dir, plan: plan, done: map[string]bool{}, lock: lock}
	if err := p.loadState(); err != nil {
		lock.Close()
		return nil, false, err
	}
	return p, true, nil
}

// lockSyncSession takes an exclusive non-blocking flock on the session
// directory. A process crash releases it automatically.
func lockSyncSession(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another sync is already running for this source/destination")
	}
	return f, nil
}

func (p *syncPersist) writePlanLocked() error {
	data, err := json.MarshalIndent(p.plan, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.dir + "/plan.json.tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(p.dir, "plan.json"))
}

func (p *syncPersist) resetState() error {
	return os.WriteFile(filepath.Join(p.dir, "state.jsonl"), nil, 0o644)
}

// loadState replays the progress journal; only ops that finished OK are
// treated as done, so failed ops are retried by a resume.
func (p *syncPersist) loadState() error {
	f, err := os.Open(filepath.Join(p.dir, "state.jsonl"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		var entry syncStateEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return fmt.Errorf("replay sync state: %w", err)
		}
		if entry.Op == "done" && entry.OK {
			p.done[syncStateKey(entry.Path, entry.Action)] = true
		}
	}
	return scanner.Err()
}

// isDone reports whether the op already finished OK.
func (p *syncPersist) isDone(path string, action syncAction) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done[syncStateKey(path, action)]
}

// markDone appends one finished op to the journal before marking it done in
// memory (journal-first, like the task store), so a crash never leaves an
// in-memory state ahead of what a resume would replay. The journal write is
// not best-effort: a persistence failure is returned so the caller can count
// the op as failed and keep the session for a retry. Failed ops are recorded
// for visibility but not marked done, so a resume retries them.
func (p *syncPersist) markDone(path string, action syncAction, err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := syncStateEntry{Op: "done", Path: path, Action: action, OK: err == nil}
	if err != nil {
		entry.Error = err.Error()
	}
	data, marshalErr := json.Marshal(entry)
	if marshalErr != nil {
		return marshalErr
	}
	f, openErr := os.OpenFile(filepath.Join(p.dir, "state.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if openErr != nil {
		return openErr
	}
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err == nil {
		p.done[syncStateKey(path, action)] = true
	}
	return nil
}

// pendingOps returns the ops that have not finished OK, in plan order.
func (p *syncPersist) pendingOps() []syncPlanEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	var pending []syncPlanEntry
	for _, op := range p.plan.Ops {
		if p.done[syncStateKey(op.Path, op.Action)] {
			continue
		}
		pending = append(pending, op)
	}
	return pending
}

// transferPending reports whether any transfer op (add/update/delete) is
// still unfinished. Conflicts and skips never block completion.
func (p *syncPersist) transferPending() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, op := range p.plan.Ops {
		switch op.Action {
		case syncAdd, syncUpdate, syncDelete:
			if !p.done[syncStateKey(op.Path, op.Action)] {
				return true
			}
		}
	}
	return false
}

// close releases the session lock, keeping the directory for a later resume.
func (p *syncPersist) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lock != nil {
		_ = p.lock.Close()
		p.lock = nil
	}
}

// remove releases the lock and deletes the session directory.
func (p *syncPersist) remove() {
	p.close()
	_ = os.RemoveAll(p.dir)
}

// pruneExpiredSyncSessions drops session directories that are not currently
// locked and have been idle beyond the TTL. Called on fresh runs; resume
// sessions are untouched.
func pruneExpiredSyncSessions() {
	root := syncPersistRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		lock, err := lockSyncSession(dir)
		if err != nil {
			continue // a live session holds the lock
		}
		newest := newestFileTime(dir)
		_ = lock.Close()
		if time.Since(newest) > syncSessionTTL {
			_ = os.RemoveAll(dir)
		}
	}
}

func newestFileTime(dir string) time.Time {
	var newest time.Time
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Now()
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}
