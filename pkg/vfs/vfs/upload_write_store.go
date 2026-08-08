package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"hash"
	"io"
	"sync"
)

func pendingUploadFromWriteStore(store *uploadStore, path string) (PendingUpload, error) {
	path = cleanVirtual(path)
	pending, ok := store.UploadByPath(path)
	if !ok {
		return PendingUpload{}, fmt.Errorf("vfs: no pending file for %s", path)
	}
	return pending, nil
}

type vfsUploadWriteHashTracker struct {
	v *VFS
}

func newVFSUploadWriteHashTracker(v *VFS) vfsUploadWriteHashTracker {
	return vfsUploadWriteHashTracker{v: v}
}

func (t vfsUploadWriteHashTracker) Start(pending PendingUpload) {
	t.v.hashes.start(pending, t.v.requiredUploadSnapshotHashes())
}

func (t vfsUploadWriteHashTracker) Write(pending PendingUpload, data []byte, off int64) {
	t.v.hashes.write(pending, data, off, t.v.requiredUploadSnapshotHashes())
}

func (t vfsUploadWriteHashTracker) Dirty(pending PendingUpload) {
	t.v.hashes.dirty(pending)
}

func (t vfsUploadWriteHashTracker) RemoveFID(fid string) {
	t.v.hashes.removeFID(fid)
}

type uploadWriteRemote interface {
	Parent(ctx context.Context, path string) (drive.Entry, string, error)
	Resolve(ctx context.Context, path string) (drive.Entry, error)
	Read(ctx context.Context, entry drive.Entry) (io.ReadCloser, error)
	InvalidateReadCache(entry drive.Entry)
}

type vfsUploadWriteRemote struct {
	v *VFS
}

func newVFSUploadWriteRemote(v *VFS) vfsUploadWriteRemote {
	return vfsUploadWriteRemote{v: v}
}

func (r vfsUploadWriteRemote) Parent(ctx context.Context, path string) (drive.Entry, string, error) {
	return r.v.parent(ctx, path)
}

func (r vfsUploadWriteRemote) Resolve(ctx context.Context, path string) (drive.Entry, error) {
	return r.v.resolve(ctx, path)
}

func (r vfsUploadWriteRemote) Read(ctx context.Context, entry drive.Entry) (io.ReadCloser, error) {
	return r.v.driver.Read(ctx, entry, 0, 0)
}

func (r vfsUploadWriteRemote) InvalidateReadCache(entry drive.Entry) {
	r.v.invalidateReadCache(entry)
}

type vfsUploadWriteRuntime struct {
	v *VFS
}

func newVFSUploadWriteRuntime(v *VFS) vfsUploadWriteRuntime {
	return vfsUploadWriteRuntime{v: v}
}

func (r vfsUploadWriteRuntime) Store() *uploadStore {
	return r.v.uploads.Store()
}

func (r vfsUploadWriteRuntime) HashTracker() vfsUploadWriteHashTracker {
	return newVFSUploadWriteHashTracker(r.v)
}

func (r vfsUploadWriteRuntime) Remote() uploadWriteRemote {
	return newVFSUploadWriteRemote(r.v)
}

type uploadHashTrackerState struct {
	mu     sync.Mutex
	byFID  map[string]*uploadHashTracker
	byPath map[string]string
}

type uploadHashTracker struct {
	path       string
	localPath  string
	nextOffset int64
	dirty      bool
	hashes     map[drive.HashAlgorithm]hash.Hash
}

func newUploadHashTrackerState() *uploadHashTrackerState {
	return &uploadHashTrackerState{
		byFID:  map[string]*uploadHashTracker{},
		byPath: map[string]string{},
	}
}

func (s *uploadHashTrackerState) start(pending PendingUpload, algorithms []drive.HashAlgorithm) {
	if s == nil || pending.FID == "" || pending.LocalPath == "" || pending.Size != 0 {
		return
	}
	hashes, _, err := newUploadSnapshotHashes(algorithms)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byFID[pending.FID] = &uploadHashTracker{
		path:      pending.Path,
		localPath: pending.LocalPath,
		hashes:    hashes,
	}
	s.byPath[pending.Path] = pending.FID
}

func (s *uploadHashTrackerState) write(pending PendingUpload, data []byte, off int64, algorithms []drive.HashAlgorithm) {
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
		hashes, _, err := newUploadSnapshotHashes(algorithms)
		if err != nil {
			return
		}
		tracker = &uploadHashTracker{
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

func (s *uploadHashTrackerState) snapshot(pending PendingUpload, algorithms []drive.HashAlgorithm) (drive.SourceHashes, bool) {
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

func (s *uploadHashTrackerState) dirty(pending PendingUpload) {
	if s == nil || pending.FID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tracker := s.byFID[pending.FID]; tracker != nil {
		tracker.dirty = true
	}
}

func (s *uploadHashTrackerState) removeFID(fid string) {
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

func (s *uploadHashTrackerState) removePath(path string) {
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

func (s *uploadHashTrackerState) renamePath(oldPath, newPath string, pending PendingUpload) {
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

func (s *uploadHashTrackerState) removeUnder(dir string) {
	if s == nil || dir == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for path, fid := range s.byPath {
		if path == dir || isPathUnder(path, dir) {
			delete(s.byPath, path)
			delete(s.byFID, fid)
		}
	}
}
