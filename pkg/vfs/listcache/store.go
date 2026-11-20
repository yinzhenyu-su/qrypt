// Package listcache persists directory listings fetched from a mount's
// backend so that later commands can serve a listing without resolving the
// directory or calling the driver again.
//
// The store is deliberately simple: one JSON file per directory, written
// atomically with a unique temporary file, and read best-effort. Every file
// carries the mount identity. A listing is served while it is within the TTL,
// so a stale file can never outlive the freshness window; any local mutation
// additionally deletes the affected listings through the commit boundary, so
// it cannot be served after a rename/remove/upload. The list generation guards
// the A-lists / B-renames / A-writes-old-list race: a fetch that started
// before a mutation is discarded instead of persisted.
//
// Entries are the raw remote listing; the provider-side remote name is stored
// alongside each entry because drive.Entry.Extra is not serializable. Local pending uploads, rename shadows
// and delayed deletes are projected on top by the view at read time; the file
// never contains them.
package listcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

const (
	// SchemaVersion is bumped when the on-disk shape changes incompatibly.
	SchemaVersion = 1
	// DefaultTTL is how long a persisted listing may be served.
	DefaultTTL = 30 * time.Second
	// DefaultMaxBytes caps one mount's persisted listings.
	DefaultMaxBytes int64 = 32 << 20
	// maxFileBytes is the largest encoded directory listing worth writing;
	// very large directories are cheaper to refetch than to serialize.
	maxFileBytes int64 = 4 << 20
	lowWatermark       = 0.8
)

// ErrDisabled reports an operation on a store that was not configured.
var ErrDisabled = errors.New("listcache: disabled")

// Stats is the observable state of one mount's list cache.
type Stats struct {
	Enabled        bool   `json:"enabled"`
	Dir            string `json:"dir,omitempty"`
	TTLSeconds     int64  `json:"ttl_seconds"`
	MaxBytes       int64  `json:"max_bytes"`
	EntryCount     int    `json:"entry_count"`
	Bytes          int64  `json:"bytes"`
	Hits           int64  `json:"hits"`
	Misses         int64  `json:"misses"`
	Puts           int64  `json:"puts"`
	Evictions      int64  `json:"evictions"`
	EvictedBytes   int64  `json:"evicted_bytes"`
	CorruptDrops   int64  `json:"corrupt_drops"`
	StaleDrops     int64  `json:"stale_drops"`
	IdentityDrops  int64  `json:"identity_drops"`
	OversizedSkips int64  `json:"oversized_skips"`
	WriteErrors    int64  `json:"write_errors"`
	LastError      string `json:"last_error,omitempty"`
}

// Store keeps one mount's persisted directory listings.
type Store struct {
	dir      string
	mount    string
	identity string
	ttl      time.Duration
	maxBytes int64

	mu    sync.Mutex
	bytes int64
	files int

	hits           atomic.Int64
	misses         atomic.Int64
	puts           atomic.Int64
	evictions      atomic.Int64
	evictedBytes   atomic.Int64
	corruptDrops   atomic.Int64
	staleDrops     atomic.Int64
	identityDrops  atomic.Int64
	oversizedSkips atomic.Int64
	writeErrors    atomic.Int64
	lastError      atomic.Pointer[string]
}

type fileFormat struct {
	Schema     int           `json:"schema"`
	Mount      string        `json:"mount"`
	Path       string        `json:"path"`
	Identity   string        `json:"identity"`
	Generation uint64        `json:"generation"`
	FetchedAt  time.Time     `json:"fetched_at"`
	Entries    []storedEntry `json:"entries"`
}

// storedEntry augments a drive.Entry with the provider-side remote name,
// which lives in the non-serializable Extra field in memory.
type storedEntry struct {
	drive.Entry
	RemoteName string `json:"remote_name,omitempty"`
}

func encodeEntries(entries []drive.Entry) []storedEntry {
	out := make([]storedEntry, len(entries))
	for i, entry := range entries {
		remote, _ := drive.EntryRemoteName(entry)
		if remote == entry.Name {
			remote = ""
		}
		out[i] = storedEntry{Entry: entry, RemoteName: remote}
	}
	return out
}

func decodeEntries(entries []storedEntry) []drive.Entry {
	out := make([]drive.Entry, len(entries))
	for i, stored := range entries {
		out[i] = drive.WrapRemoteName(stored.Entry, stored.RemoteName)
	}
	return out
}

// NewStore opens (and creates) a store under dir. A non-positive ttl or an
// empty dir disables the cache and returns (nil, nil); callers treat a nil
// store as "not configured".
func NewStore(dir, mount, identity string, ttl time.Duration, maxBytes int64) (*Store, error) {
	if dir == "" || ttl <= 0 {
		return nil, nil
	}
	if maxBytes < 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("listcache: create dir: %w", err)
	}
	s := &Store{
		dir:      dir,
		mount:    mount,
		identity: identity,
		ttl:      ttl,
		maxBytes: maxBytes,
	}
	s.recount()
	return s, nil
}

// Enabled reports whether the store can serve listings.
func (s *Store) Enabled() bool { return s != nil }

// Dir returns the store directory.
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// TTL returns the configured freshness window.
func (s *Store) TTL() time.Duration {
	if s == nil {
		return 0
	}
	return s.ttl
}

// Get returns a fresh listing for path. A file is served while it is within
// the TTL and was written for the same mount identity. The generation field
// guards concurrent writes (see Put), not reads: a listing persisted by a
// previous owner process is still valid because the single-writer owner lock
// guarantees no other process mutated the mount in between.
func (s *Store) Get(path string, now time.Time) ([]drive.Entry, bool) {
	if s == nil {
		return nil, false
	}
	path = vfstypes.CleanVirtualPath(path)
	raw, err := os.ReadFile(s.filePath(path))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.recordError("read", err)
		}
		s.misses.Add(1)
		return nil, false
	}
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		s.dropFile(path, "corrupt")
		s.misses.Add(1)
		return nil, false
	}
	if f.Schema != SchemaVersion || f.Mount != s.mount || f.Path != path {
		s.dropFile(path, "corrupt")
		s.misses.Add(1)
		return nil, false
	}
	if s.identity != "" && f.Identity != "" && f.Identity != s.identity {
		s.identityDrops.Add(1)
		s.dropFile(path, "identity")
		s.misses.Add(1)
		return nil, false
	}
	if now.Sub(f.FetchedAt) >= s.ttl || f.FetchedAt.After(now.Add(time.Minute)) {
		s.dropFile(path, "expired")
		s.misses.Add(1)
		return nil, false
	}
	s.hits.Add(1)
	return decodeEntries(f.Entries), true
}

// Put atomically persists a raw remote listing fetched under generation.
// Callers must pass the generation observed before the fetch; the store
// itself does not know the current generation, the owner does.
func (s *Store) Put(path string, entries []drive.Entry, generation uint64, fetchedAt time.Time) {
	if s == nil || len(entries) == 0 {
		return
	}
	path = vfstypes.CleanVirtualPath(path)
	f := fileFormat{
		Schema:     SchemaVersion,
		Mount:      s.mount,
		Path:       path,
		Identity:   s.identity,
		Generation: generation,
		FetchedAt:  fetchedAt,
		Entries:    encodeEntries(entries),
	}
	raw, err := json.Marshal(f)
	if err != nil {
		s.recordError("marshal", err)
		return
	}
	if int64(len(raw)) > maxFileBytes {
		s.oversizedSkips.Add(1)
		return
	}
	if err := s.writeFile(path, raw); err != nil {
		s.recordError("write", err)
		return
	}
	s.puts.Add(1)
	s.evictIfNeeded()
}

// Delete removes one directory's cached listing.
func (s *Store) Delete(path string) {
	if s == nil {
		return
	}
	s.dropFile(vfstypes.CleanVirtualPath(path), "delete")
}

// Clear removes every cached listing for the mount.
func (s *Store) Clear() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("listcache: clear: %w", err)
	}
	var errs []error
	for _, entry := range entries {
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	s.recountLocked()
	return errors.Join(errs...)
}

// Close releases the store. Files stay on disk for the next process.
func (s *Store) Close() error { return nil }

// Stats snapshots counters and current size.
func (s *Store) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	s.mu.Lock()
	entries, bytes := s.files, s.bytes
	s.mu.Unlock()
	st := Stats{
		Enabled:        true,
		Dir:            s.dir,
		TTLSeconds:     int64(s.ttl.Seconds()),
		MaxBytes:       s.maxBytes,
		EntryCount:     entries,
		Bytes:          bytes,
		Hits:           s.hits.Load(),
		Misses:         s.misses.Load(),
		Puts:           s.puts.Load(),
		Evictions:      s.evictions.Load(),
		EvictedBytes:   s.evictedBytes.Load(),
		CorruptDrops:   s.corruptDrops.Load(),
		StaleDrops:     s.staleDrops.Load(),
		IdentityDrops:  s.identityDrops.Load(),
		OversizedSkips: s.oversizedSkips.Load(),
		WriteErrors:    s.writeErrors.Load(),
	}
	if ptr := s.lastError.Load(); ptr != nil {
		st.LastError = *ptr
	}
	return st
}

func (s *Store) filePath(path string) string {
	sum := sha256.Sum256([]byte(s.mount + "\x00" + path))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

func (s *Store) writeFile(path string, raw []byte) error {
	tmp, err := os.CreateTemp(s.dir, "*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}
	if _, err := tmp.Write(raw); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, s.filePath(path)); err != nil {
		_ = os.Remove(name)
		return err
	}
	s.mu.Lock()
	s.recountLocked()
	s.mu.Unlock()
	return nil
}

func (s *Store) dropFile(path, reason string) {
	switch reason {
	case "corrupt":
		s.corruptDrops.Add(1)
	case "stale":
		// counted by caller
	case "identity":
		// counted by caller
	}
	_ = os.Remove(s.filePath(path))
	s.mu.Lock()
	s.recountLocked()
	s.mu.Unlock()
}

func (s *Store) recordError(op string, err error) {
	s.writeErrors.Add(1)
	msg := op + ": " + err.Error()
	s.lastError.Store(&msg)
}

func (s *Store) recount() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recountLocked()
}

func (s *Store) recountLocked() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	var files int
	var bytes int64
	for _, entry := range entries {
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files++
		bytes += info.Size()
	}
	s.files = files
	s.bytes = bytes
}

func (s *Store) evictIfNeeded() {
	if s.maxBytes <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bytes <= s.maxBytes {
		return
	}
	type candidate struct {
		name  string
		size  int64
		mtime time.Time
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{name: entry.Name(), size: info.Size(), mtime: info.ModTime()})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mtime.Before(candidates[j].mtime) })
	target := int64(float64(s.maxBytes) * lowWatermark)
	for _, cand := range candidates {
		if s.bytes <= target {
			break
		}
		if err := os.Remove(filepath.Join(s.dir, cand.name)); err != nil {
			continue
		}
		s.bytes -= cand.size
		s.files--
		s.evictions.Add(1)
		s.evictedBytes.Add(cand.size)
	}
}
