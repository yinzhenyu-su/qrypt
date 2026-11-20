// Package ownerlock implements the single-writer ownership lock that keeps one
// qrypt runtime responsible for a state root at a time.
//
// The lock is advisory at the API level but exclusive at the OS level: it uses
// flock(2) on Unix and LockFileEx on Windows. The on-disk metadata is only
// informational. A process that cannot take the lock reads it to explain the
// conflict (pid, configuration fingerprint) and the caller decides to attach to
// the running runtime or to fail with an actionable error.
package ownerlock

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MetaSchemaVersion is the on-disk schema version of the owner metadata.
const MetaSchemaVersion = 1

// LockFileName is the lock file created inside a state root.
const LockFileName = "owner.lock"

// Meta describes the runtime that owns a state root. It is written after the
// lock is acquired and is never authoritative: the OS lock decides ownership.
type Meta struct {
	Schema       int       `json:"schema"`
	PID          int       `json:"pid"`
	QryptVersion string    `json:"qrypt_version,omitempty"`
	ConfigPath   string    `json:"config_path,omitempty"`
	Fingerprint  string    `json:"fingerprint,omitempty"`
	WorkDir      string    `json:"work_dir,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
}

// Dir is one writable directory the owning runtime writes to. Overlapping
// entries are ambiguous (two runtimes could write the same files while
// considering themselves separate owners), so Acquire rejects them.
type Dir struct {
	Label string
	Path  string
}

// Lease is a held ownership lock. Release it after the filesystem that used
// the state root has closed.
type Lease struct {
	mu       sync.Mutex
	path     string
	workDir  string
	released bool
	record   *leaseRecord
}

// leaseRecord is the process-wide view of one held OS lock. Several leases in
// the same process (fs copy across mounts, Core.Reload opening a replacement
// before closing the old Core) share one record and one OS lock; the OS lock
// is released when the last lease is.
type leaseRecord struct {
	refs int
	meta Meta
	lock *osLease
}

type osLease struct {
	mu       sync.Mutex
	file     *os.File
	released bool
}

func (l *osLease) release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	if l.file == nil {
		return nil
	}
	err := unlockFile(l.file)
	if closeErr := l.file.Close(); err == nil {
		err = closeErr
	}
	l.file = nil
	return err
}

// processRegistry tracks this process's held locks so a second Acquire in the
// same process shares the OS lock instead of failing against itself.
var processRegistry = struct {
	mu   sync.Mutex
	held map[string]*leaseRecord
}{held: map[string]*leaseRecord{}}

// Path returns the lock file path.
func (l *Lease) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Release releases this lease. Releasing the last lease for a state root
// releases the OS lock. It is idempotent and safe on a nil lease.
func (l *Lease) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	if l.record == nil {
		return nil
	}
	processRegistry.mu.Lock()
	l.record.refs--
	if l.record.refs > 0 {
		processRegistry.mu.Unlock()
		return nil
	}
	delete(processRegistry.held, l.workDir)
	processRegistry.mu.Unlock()
	return l.record.lock.release()
}

// HeldError reports that another runtime owns the state root.
type HeldError struct {
	Path   string
	Holder Meta
}

func (e *HeldError) Error() string {
	if e.Holder.PID > 0 {
		return fmt.Sprintf("qrypt state is already owned by another runtime (pid %d, config %q); stop it before starting another runtime", e.Holder.PID, e.Holder.ConfigPath)
	}
	return "qrypt state is already owned by another runtime; stop it before starting another runtime"
}

// MismatchError reports that the running owner was started with a different
// configuration. Both runtimes cannot share the state root, so the running one
// must be stopped and restarted with the current configuration.
type MismatchError struct {
	Path   string
	Holder Meta
	Want   string
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("qrypt state is owned by pid %d with a different configuration (owner fingerprint %s, this command %s); stop that runtime and restart it with the current configuration", e.Holder.PID, e.Holder.Fingerprint, e.Want)
}

// Path returns the lock file path for a state root.
func Path(workDir string) string {
	return filepath.Join(workDir, LockFileName)
}

// Acquire takes the ownership lock for workDir. It never blocks: a held lock is
// reported immediately so the caller can either attach to the running runtime
// or explain the conflict. dirs are validated to be non-overlapping before the
// lock is taken, so a misconfigured layout never becomes an owner.
func Acquire(workDir string, meta Meta, dirs []Dir) (*Lease, error) {
	if strings.TrimSpace(workDir) == "" {
		return nil, fmt.Errorf("ownerlock: work dir required")
	}
	if err := ValidateDisjoint(dirs); err != nil {
		return nil, err
	}
	path := Path(workDir)
	key := resolvePath(workDir)
	if meta.Schema == 0 {
		meta.Schema = MetaSchemaVersion
	}
	if meta.WorkDir == "" {
		meta.WorkDir = workDir
	}

	// Serialize in-process acquisitions: the registry check, the OS lock, and
	// the registration must happen as one step. Otherwise two goroutines can
	// both try to lock the same file and the loser would report a bogus
	// cross-process conflict against its own process.
	processRegistry.mu.Lock()
	defer processRegistry.mu.Unlock()

	if record, ok := processRegistry.held[key]; ok {
		if record.meta.PID == meta.PID && record.meta.Fingerprint == meta.Fingerprint {
			record.refs++
			return &Lease{path: path, workDir: key, record: record}, nil
		}
		if meta.Fingerprint != "" && record.meta.Fingerprint != "" && record.meta.Fingerprint != meta.Fingerprint {
			return nil, &MismatchError{Path: path, Holder: record.meta, Want: meta.Fingerprint}
		}
		return nil, &HeldError{Path: path, Holder: record.meta}
	}

	osLock, err := acquireOSLock(path, meta)
	if err != nil {
		return nil, err
	}
	record := &leaseRecord{refs: 1, meta: meta, lock: osLock}
	processRegistry.held[key] = record
	return &Lease{path: path, workDir: key, record: record}, nil
}

func acquireOSLock(path string, meta Meta) (*osLease, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	locked, err := tryLockFile(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !locked {
		holder := readMeta(file)
		_ = file.Close()
		if meta.Fingerprint != "" && holder.Fingerprint != "" && holder.Fingerprint != meta.Fingerprint {
			return nil, &MismatchError{Path: path, Holder: holder, Want: meta.Fingerprint}
		}
		return nil, &HeldError{Path: path, Holder: holder}
	}
	payload, err := json.Marshal(meta)
	if err == nil {
		err = writeMeta(file, payload)
	}
	if err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, err
	}
	return &osLease{file: file}, nil
}

// Peek reports whether the lock for workDir is currently held and returns the
// last metadata written to it. It never takes ownership.
func Peek(workDir string) (bool, Meta) {
	processRegistry.mu.Lock()
	if record, ok := processRegistry.held[resolvePath(workDir)]; ok {
		meta := record.meta
		processRegistry.mu.Unlock()
		return true, meta
	}
	processRegistry.mu.Unlock()
	path := Path(workDir)
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, Meta{}
	}
	defer func() { _ = file.Close() }()
	holder := readMeta(file)
	locked, err := tryLockFile(file)
	if err != nil {
		return false, holder
	}
	if !locked {
		return true, holder
	}
	_ = unlockFile(file)
	return false, holder
}

// FingerprintBytes returns the configuration fingerprint for raw config bytes.
func FingerprintBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// FingerprintFile returns the configuration fingerprint of a file.
func FingerprintFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return FingerprintBytes(data), nil
}

// ValidateDisjoint rejects a writable-directory set where one directory
// contains another. Overlapping directories mean two runtimes cannot be told
// apart by the lock, so the layout itself must be unambiguous.
func ValidateDisjoint(dirs []Dir) error {
	for i := 0; i < len(dirs); i++ {
		if strings.TrimSpace(dirs[i].Path) == "" {
			continue
		}
		left := resolvePath(dirs[i].Path)
		for j := i + 1; j < len(dirs); j++ {
			if strings.TrimSpace(dirs[j].Path) == "" {
				continue
			}
			right := resolvePath(dirs[j].Path)
			if rel, err := filepath.Rel(left, right); err == nil && !escapes(rel) {
				return overlapError(dirs[i], dirs[j])
			}
			if rel, err := filepath.Rel(right, left); err == nil && !escapes(rel) {
				return overlapError(dirs[i], dirs[j])
			}
		}
	}
	return nil
}

func overlapError(left, right Dir) error {
	return fmt.Errorf("ownerlock: writable directories %q (%s) and %q (%s) overlap; configure distinct storage directories", left.Label, left.Path, right.Label, right.Path)
}

func escapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func writeMeta(file *os.File, payload []byte) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := file.Write(append(payload, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func readMeta(file *os.File) Meta {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Meta{}
	}
	data, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return Meta{}
	}
	var meta Meta
	if json.Unmarshal(bytes.TrimSpace(data), &meta) != nil {
		return Meta{}
	}
	return meta
}

// resolvePath resolves symlinks on the deepest existing ancestor and re-appends
// the still-missing suffix, so directories that do not exist yet (first run)
// still compare correctly.
func resolvePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	parent := absolute
	var suffix []string
	for {
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		suffix = append([]string{filepath.Base(parent)}, suffix...)
		parent = next
		if _, err := os.Lstat(parent); err == nil {
			break
		}
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return absolute
	}
	return filepath.Join(append([]string{resolved}, suffix...)...)
}
