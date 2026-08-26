package upload

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// HashTracker accumulates running hashes for staged uploads written in
// sequence, so later snapshots can be served incrementally without re-reading
// the staging file. It implements HashOps, keeping the service's
// rename/delete bookkeeping for tracked paths in sync.
type HashTracker struct {
	mu     sync.Mutex
	byFID  map[string]*hashTracker
	byPath map[string]string
}

type hashTracker struct {
	path       string
	localPath  string
	nextOffset int64
	dirty      bool
	hashes     map[drive.HashAlgorithm]hash.Hash
}

// NewHashTracker returns an empty tracker.
func NewHashTracker() *HashTracker {
	return &HashTracker{
		byFID:  map[string]*hashTracker{},
		byPath: map[string]string{},
	}
}

// Start begins tracking sequential hashes for a staged upload.
func (s *HashTracker) Start(pending PendingUpload, algorithms []drive.HashAlgorithm) {
	if s == nil || pending.FID == "" || pending.LocalPath == "" || pending.Size != 0 {
		return
	}
	hashes, _, err := NewSnapshotHashes(algorithms)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byFID[pending.FID] = &hashTracker{
		path:      pending.Path,
		localPath: pending.LocalPath,
		hashes:    hashes,
	}
	s.byPath[pending.Path] = pending.FID
}

// Write feeds a sequential chunk into the tracked hash state.
func (s *HashTracker) Write(pending PendingUpload, data []byte, off int64, algorithms []drive.HashAlgorithm) {
	if s == nil || len(data) == 0 || pending.FID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tracker := s.byFID[pending.FID]
	if tracker == nil {
		if pending.Size != 0 || off != 0 {
			return
		}
		hashes, _, err := NewSnapshotHashes(algorithms)
		if err != nil {
			return
		}
		tracker = &hashTracker{
			path:      pending.Path,
			localPath: pending.LocalPath,
			hashes:    hashes,
		}
		s.byFID[pending.FID] = tracker
		s.byPath[pending.Path] = pending.FID
	}
	if tracker.dirty || tracker.localPath != pending.LocalPath || off != tracker.nextOffset {
		tracker.dirty = true
		return
	}
	for _, h := range tracker.hashes {
		_, _ = h.Write(data)
	}
	tracker.nextOffset += int64(len(data))
}

// Snapshot returns the accumulated hashes when the tracked writes cover the
// whole staged file, or false when the state is dirty/incomplete.
func (s *HashTracker) Snapshot(pending PendingUpload, algorithms []drive.HashAlgorithm) (drive.SourceHashes, bool) {
	if s == nil || pending.FID == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tracker := s.byFID[pending.FID]
	if tracker == nil || tracker.dirty || tracker.path != pending.Path || tracker.localPath != pending.LocalPath || tracker.nextOffset != pending.Size {
		return nil, false
	}
	hashes := make(drive.SourceHashes, len(algorithms))
	for _, algorithm := range algorithms {
		h := tracker.hashes[algorithm]
		if h == nil {
			return nil, false
		}
		hashes[algorithm] = h.Sum(nil)
	}
	return hashes, true
}

// Dirty marks the tracked state as non-incremental (writes out of sequence).
func (s *HashTracker) Dirty(pending PendingUpload) {
	if s == nil || pending.FID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tracker := s.byFID[pending.FID]; tracker != nil {
		tracker.dirty = true
	}
}

// RemoveFID stops tracking a file by its remote ID.
func (s *HashTracker) RemoveFID(fid string) {
	if s == nil || fid == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tracker := s.byFID[fid]; tracker != nil {
		if s.byPath[tracker.path] == fid {
			delete(s.byPath, tracker.path)
		}
	}
	delete(s.byFID, fid)
}

// RemovePath stops tracking a virtual path.
func (s *HashTracker) RemovePath(path string) {
	if s == nil || path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if fid := s.byPath[path]; fid != "" {
		delete(s.byFID, fid)
	}
	delete(s.byPath, path)
}

// RenamePath moves the tracked state to a new virtual path / staging file.
func (s *HashTracker) RenamePath(oldPath, newPath string, pending PendingUpload) {
	if s == nil || oldPath == "" || newPath == "" || pending.FID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tracker := s.byFID[pending.FID]
	if tracker == nil {
		return
	}
	if s.byPath[oldPath] == pending.FID {
		delete(s.byPath, oldPath)
	}
	tracker.path = newPath
	tracker.localPath = pending.LocalPath
	s.byPath[newPath] = pending.FID
}

// RemoveUnder stops tracking every path at or below dir.
func (s *HashTracker) RemoveUnder(dir string) {
	if s == nil || dir == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for path, fid := range s.byPath {
		if path == dir || vfstypes.IsPathUnder(path, dir) {
			delete(s.byPath, path)
			delete(s.byFID, fid)
		}
	}
}

var _ HashOps = (*HashTracker)(nil)

// NewSnapshotHashes builds one running hash per requested algorithm, plus a
// writer per hash for incremental feeding.
func NewSnapshotHashes(algorithms []drive.HashAlgorithm) (map[drive.HashAlgorithm]hash.Hash, []io.Writer, error) {
	hashes := make(map[drive.HashAlgorithm]hash.Hash, len(algorithms))
	writers := make([]io.Writer, 0, len(algorithms))
	for _, algorithm := range algorithms {
		var h hash.Hash
		switch algorithm {
		case drive.HashMD5:
			h = md5.New()
		case drive.HashSHA1:
			h = sha1.New()
		case drive.HashSHA256:
			h = sha256.New()
		default:
			return nil, nil, fmt.Errorf("vfs: unsupported upload hash algorithm %q", algorithm)
		}
		hashes[algorithm] = h
		writers = append(writers, h)
	}
	return hashes, writers, nil
}
