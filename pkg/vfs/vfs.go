// Package vfs provides the platform-independent file API used by CLI, FUSE,
// and future mobile adapters.
package vfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const readChunkSize = 512 * 1024
const readPrefetchRadius = 1
const readPrefetchLimit = 2
const readPrefetchChunks = 8
const listCacheTTL = 10 * time.Second
const uploadDebounceDelay = 5 * time.Second
const deleteDebounceDelay = 2 * time.Second
const restoredDirTTL = 60 * time.Second
const directoryCopyHideTTL = 10 * time.Minute

type Options struct {
	CacheDir      string
	CacheMaxBytes int64
	RootID        string
	UploadDelay   time.Duration
	DeleteDelay   time.Duration
}

type VFS struct {
	driver drive.Driver
	writer drive.Writer
	upload drive.Uploader
	cache  *Cache
	rootID string

	mu      sync.RWMutex
	entries map[string]drive.Entry
	lists   map[string]listCacheEntry
	queue   chan PendingFile

	uploadDelay  time.Duration
	uploadMu     sync.Mutex
	uploadTimers map[string]*time.Timer

	deleteDelay  time.Duration
	deleteMu     sync.Mutex
	deleteTimers map[string]*time.Timer
	deleted      map[string]drive.Entry
	overlayOps   map[string]overlayOp
	restoredDirs map[string]time.Time
	copyHidden   map[string]map[string]time.Time

	prefetchMu  sync.Mutex
	prefetching map[string]struct{}
	prefetchSem chan struct{}

	chunkLoadMu sync.Mutex
	chunkLoads  map[string]*chunkLoad

	windowLoadMu sync.Mutex
	windowLoads  map[string]*windowLoad
}

type overlayOp struct {
	oldPath string
	newPath string
	entryID string
	isDir   bool
	oldGone bool
	newSeen bool
}

func New(driver drive.Driver, opts Options) (*VFS, error) {
	if opts.RootID == "" {
		opts.RootID = "0"
	}
	if opts.UploadDelay == 0 {
		opts.UploadDelay = uploadDebounceDelay
	}
	if opts.DeleteDelay == 0 {
		opts.DeleteDelay = deleteDebounceDelay
	}
	cache, err := NewCache(opts.CacheDir, opts.CacheMaxBytes)
	if err != nil {
		return nil, err
	}
	v := &VFS{
		driver:       driver,
		cache:        cache,
		rootID:       opts.RootID,
		entries:      map[string]drive.Entry{},
		lists:        map[string]listCacheEntry{},
		queue:        make(chan PendingFile, 128),
		uploadDelay:  opts.UploadDelay,
		uploadTimers: map[string]*time.Timer{},
		deleteDelay:  opts.DeleteDelay,
		deleteTimers: map[string]*time.Timer{},
		deleted:      map[string]drive.Entry{},
		overlayOps:   map[string]overlayOp{},
		restoredDirs: map[string]time.Time{},
		copyHidden:   map[string]map[string]time.Time{},
		prefetching:  map[string]struct{}{},
		prefetchSem:  make(chan struct{}, readPrefetchLimit),
		chunkLoads:   map[string]*chunkLoad{},
		windowLoads:  map[string]*windowLoad{},
	}
	v.writer, _ = driver.(drive.Writer)
	v.upload, _ = driver.(drive.Uploader)
	v.entries["/"] = drive.Entry{ID: opts.RootID, Name: "/", IsDir: true}
	return v, nil
}

func (v *VFS) Start(ctx context.Context) {
	go v.uploadWorker(ctx)
	v.Resume(ctx)
}

func (v *VFS) Resume(ctx context.Context) {
	for _, pending := range v.cache.Pending() {
		v.enqueue(pending)
	}
}

func (v *VFS) Space(ctx context.Context) (drive.Space, error) {
	querier, ok := v.driver.(drive.SpaceQuerier)
	if !ok {
		return drive.Space{}, fmt.Errorf("vfs: driver does not support space query")
	}
	return querier.Space(ctx)
}

func (v *VFS) Stat(ctx context.Context, path string) (drive.Entry, error) {
	path = cleanVirtual(path)
	if pending, err := v.pending(path); err == nil {
		return drive.Entry{
			ID:       pending.FID,
			ParentID: pending.ParentID,
			Name:     pending.Name,
			IsDir:    false,
			Size:     pending.Size,
		}, nil
	}
	return v.resolve(ctx, path)
}

func (v *VFS) List(ctx context.Context, path string) ([]drive.Entry, error) {
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	entries, err := v.listChildren(ctx, path, entry.ID)
	if err != nil {
		return nil, err
	}
	entries = v.withPendingChildren(path, entries)
	return entries, nil
}

func (v *VFS) Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	if pending, err := v.pending(path); err == nil {
		if err := v.cache.staging.flush(pending.LocalPath); err != nil {
			return nil, err
		}
		f, err := os.Open(pending.LocalPath)
		if err != nil {
			return nil, err
		}
		if offset > 0 {
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				f.Close()
				return nil, err
			}
		}
		if size > 0 {
			return struct {
				io.Reader
				io.Closer
			}{Reader: io.LimitReader(f, size), Closer: f}, nil
		}
		return f, nil
	}
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	if entry.IsDir {
		return nil, fmt.Errorf("vfs: %s is a directory", path)
	}
	data, startChunk, endChunk, err := v.readRange(ctx, entry, offset, size)
	if err != nil {
		return nil, err
	}
	v.prefetchAdjacentChunks(ctx, entry, startChunk, endChunk)
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (v *VFS) readRange(ctx context.Context, entry drive.Entry, offset, size int64) ([]byte, int64, int64, error) {
	if offset < 0 || size < 0 {
		return nil, 0, 0, fmt.Errorf("vfs: read offset and size must be non-negative")
	}
	startChunk := offset / readChunkSize
	endChunk := startChunk
	var out bytes.Buffer
	pos := offset
	end, endKnown := readEnd(offset, size, entry.Size)
	for {
		if endKnown && pos >= end {
			break
		}
		chunkIndex := pos / readChunkSize
		chunk, err := v.readChunk(ctx, entry, chunkIndex)
		if err != nil {
			return nil, startChunk, endChunk, err
		}
		if len(chunk) == 0 {
			break
		}
		chunkStart := chunkIndex * readChunkSize
		start := pos - chunkStart
		if start >= int64(len(chunk)) {
			break
		}
		stop := int64(len(chunk))
		if endKnown && end-chunkStart < stop {
			stop = end - chunkStart
		}
		if stop > start {
			out.Write(chunk[start:stop])
			endChunk = chunkIndex
		}
		if len(chunk) < readChunkSize || (endKnown && chunkStart+stop >= end) {
			break
		}
		pos = chunkStart + stop
	}
	return out.Bytes(), startChunk, endChunk, nil
}

func readEnd(offset, size, entrySize int64) (int64, bool) {
	if size > 0 {
		end := offset + size
		if entrySize > 0 && end > entrySize {
			end = entrySize
		}
		return end, true
	}
	if entrySize > 0 {
		return entrySize, true
	}
	return 0, false
}

func (v *VFS) readChunk(ctx context.Context, entry drive.Entry, index int64) ([]byte, error) {
	if cached, ok, err := v.cache.GetChunk(entry.ID, index); err != nil {
		return nil, err
	} else if ok {
		return cached, nil
	}
	if data, ok, err := v.waitWindow(ctx, entry.ID, index); err != nil {
		return nil, err
	} else if ok {
		if data != nil {
			return data, nil
		}
		if cached, ok, err := v.cache.GetChunk(entry.ID, index); err != nil {
			return nil, err
		} else if ok {
			return cached, nil
		}
	}
	return v.loadChunk(ctx, entry, index)
}

type chunkLoad struct {
	done chan struct{}
	data []byte
	err  error
}

func (v *VFS) loadChunk(ctx context.Context, entry drive.Entry, index int64) ([]byte, error) {
	key := readChunkKey(entry.ID, index)
	v.chunkLoadMu.Lock()
	if load := v.chunkLoads[key]; load != nil {
		v.chunkLoadMu.Unlock()
		select {
		case <-load.done:
			return load.data, load.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	load := &chunkLoad{done: make(chan struct{})}
	v.chunkLoads[key] = load
	v.chunkLoadMu.Unlock()

	load.data, load.err = v.fetchChunk(ctx, entry, index)
	close(load.done)

	v.chunkLoadMu.Lock()
	delete(v.chunkLoads, key)
	v.chunkLoadMu.Unlock()
	return load.data, load.err
}

func (v *VFS) fetchChunk(ctx context.Context, entry drive.Entry, index int64) ([]byte, error) {
	rc, err := v.driver.Read(ctx, entry, index*readChunkSize, readChunkSize)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(rc)
	closeErr := rc.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > 0 {
		_ = v.cache.PutChunk(entry.ID, index, data)
	}
	return data, nil
}

func (v *VFS) prefetchAdjacentChunks(ctx context.Context, entry drive.Entry, startChunk, endChunk int64) {
	v.prefetchChunk(ctx, entry, startChunk-readPrefetchRadius)
	v.prefetchWindow(ctx, entry, endChunk+1, readPrefetchChunks)
}

type windowLoad struct {
	fid   string
	start int64
	end   int64
	done  chan struct{}
	data  map[int64][]byte
	err   error
}

func (v *VFS) prefetchWindow(ctx context.Context, entry drive.Entry, startIndex int64, count int) {
	if startIndex < 0 || count <= 0 {
		return
	}
	if entry.Size > 0 && startIndex*readChunkSize >= entry.Size {
		return
	}
	endIndex := startIndex + int64(count) - 1
	for index := startIndex; index <= endIndex; index++ {
		if entry.Size > 0 && index*readChunkSize >= entry.Size {
			endIndex = index - 1
			break
		}
		if _, ok, err := v.cache.GetChunk(entry.ID, index); err != nil || ok {
			if index == startIndex {
				startIndex++
			}
			continue
		}
	}
	if endIndex < startIndex {
		return
	}
	key := readWindowKey(entry.ID, startIndex, endIndex)
	v.prefetchMu.Lock()
	if _, ok := v.prefetching[key]; ok {
		v.prefetchMu.Unlock()
		return
	}
	v.prefetching[key] = struct{}{}
	v.prefetchMu.Unlock()
	select {
	case v.prefetchSem <- struct{}{}:
	default:
		v.prefetchMu.Lock()
		delete(v.prefetching, key)
		v.prefetchMu.Unlock()
		return
	}

	load := &windowLoad{fid: entry.ID, start: startIndex, end: endIndex, done: make(chan struct{})}
	v.windowLoadMu.Lock()
	v.windowLoads[key] = load
	v.windowLoadMu.Unlock()

	go func() {
		defer func() {
			close(load.done)
			v.windowLoadMu.Lock()
			delete(v.windowLoads, key)
			v.windowLoadMu.Unlock()
			<-v.prefetchSem
			v.prefetchMu.Lock()
			delete(v.prefetching, key)
			v.prefetchMu.Unlock()
		}()
		load.data, load.err = v.fetchChunkWindow(context.WithoutCancel(ctx), entry, startIndex, endIndex)
	}()
}

func (v *VFS) fetchChunkWindow(ctx context.Context, entry drive.Entry, startIndex, endIndex int64) (map[int64][]byte, error) {
	offset := startIndex * readChunkSize
	size := (endIndex - startIndex + 1) * readChunkSize
	if entry.Size > 0 && offset+size > entry.Size {
		size = entry.Size - offset
	}
	if size <= 0 {
		return nil, nil
	}
	rc, err := v.driver.Read(ctx, entry, offset, size)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(rc)
	closeErr := rc.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	chunks := map[int64][]byte{}
	for index := startIndex; len(data) > 0 && index <= endIndex; index++ {
		chunkSize := readChunkSize
		if len(data) < chunkSize {
			chunkSize = len(data)
		}
		chunk := make([]byte, chunkSize)
		copy(chunk, data[:chunkSize])
		chunks[index] = chunk
		_ = v.cache.PutChunk(entry.ID, index, chunk)
		data = data[chunkSize:]
	}
	return chunks, nil
}

func (v *VFS) waitWindow(ctx context.Context, fid string, index int64) ([]byte, bool, error) {
	v.windowLoadMu.Lock()
	var load *windowLoad
	for _, candidate := range v.windowLoads {
		if candidate.fid == fid && index >= candidate.start && index <= candidate.end {
			load = candidate
			break
		}
	}
	v.windowLoadMu.Unlock()
	if load == nil {
		return nil, false, nil
	}
	select {
	case <-load.done:
		if load.err != nil {
			return nil, true, load.err
		}
		return load.data[index], true, nil
	case <-ctx.Done():
		return nil, true, ctx.Err()
	}
}

func (v *VFS) prefetchChunk(ctx context.Context, entry drive.Entry, index int64) {
	if index < 0 {
		return
	}
	if entry.Size > 0 && index*readChunkSize >= entry.Size {
		return
	}
	if _, ok, err := v.cache.GetChunk(entry.ID, index); err != nil || ok {
		return
	}
	key := readChunkKey(entry.ID, index)
	v.prefetchMu.Lock()
	if _, ok := v.prefetching[key]; ok {
		v.prefetchMu.Unlock()
		return
	}
	v.prefetching[key] = struct{}{}
	v.prefetchMu.Unlock()
	select {
	case v.prefetchSem <- struct{}{}:
	default:
		v.prefetchMu.Lock()
		delete(v.prefetching, key)
		v.prefetchMu.Unlock()
		return
	}

	go func() {
		defer func() {
			<-v.prefetchSem
			v.prefetchMu.Lock()
			delete(v.prefetching, key)
			v.prefetchMu.Unlock()
		}()
		_, _ = v.loadChunk(context.WithoutCancel(ctx), entry, index)
	}()
}

func readChunkKey(fid string, index int64) string {
	return fid + "\x00" + strconv.FormatInt(index, 10)
}

func readWindowKey(fid string, start, end int64) string {
	return fid + "\x00" + strconv.FormatInt(start, 10) + "\x00" + strconv.FormatInt(end, 10)
}

func (v *VFS) Create(ctx context.Context, path string) error {
	if v.upload == nil {
		return fmt.Errorf("vfs: driver does not support upload")
	}
	path = cleanVirtual(path)
	v.restoreDeletedAncestor(filepath.Dir(path))
	v.cancelDeletedFile(path)
	parent, name, err := v.parent(ctx, path)
	if err != nil {
		return err
	}
	v.unhideCopyChild(filepath.Dir(path), name)
	fid := stagingFID(path)
	localPath, err := v.cache.staging.create(fid)
	if err != nil {
		return err
	}
	pending := PendingFile{Path: path, FID: fid, ParentID: parent.ID, Name: name, LocalPath: localPath}
	return v.cache.SavePending(pending)
}

func (v *VFS) WriteAt(ctx context.Context, path string, data []byte, off int64) (int, error) {
	path = cleanVirtual(path)
	pending, err := v.pending(path)
	if err != nil {
		if entry, resolveErr := v.resolve(ctx, path); resolveErr == nil && !entry.IsDir {
			if err := v.stageExisting(ctx, path); err != nil {
				return 0, err
			}
		} else {
			if err := v.Create(ctx, path); err != nil {
				return 0, err
			}
		}
		pending, err = v.pending(path)
		if err != nil {
			return 0, err
		}
	}
	n, err := v.cache.staging.writeAt(pending.LocalPath, data, off)
	if err != nil {
		return n, err
	}
	size, _ := v.cache.staging.size(pending.LocalPath)
	pending.Size = size
	if err := v.cache.SavePending(pending); err != nil {
		return n, err
	}
	return n, nil
}

func (v *VFS) Flush(ctx context.Context, path string) error {
	path = cleanVirtual(path)
	pending, err := v.pending(path)
	if err != nil {
		return nil
	}
	if err := v.cache.staging.flush(pending.LocalPath); err != nil {
		return err
	}
	size, err := v.cache.staging.size(pending.LocalPath)
	if err != nil {
		return err
	}
	pending.Size = size
	if err := v.cache.SavePending(pending); err != nil {
		return err
	}
	v.enqueue(pending)
	return nil
}

func (v *VFS) PrepareDirectoryCopy(ctx context.Context, path string) error {
	path = cleanVirtual(path)
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	if !entry.IsDir {
		return fmt.Errorf("vfs: %s is not a directory", path)
	}
	hideNames := map[string]time.Time{}
	if entries, err := v.driver.List(ctx, entry.ID); err == nil {
		expires := time.Now().Add(directoryCopyHideTTL)
		for _, child := range entries {
			if !isAppleMetadataName(child.Name) {
				hideNames[child.Name] = expires
			}
		}
	}
	v.cancelChildUploads(path)
	if err := v.cache.RemovePendingUnder(path); err != nil {
		return err
	}
	v.mu.Lock()
	for cachedPath, cachedEntry := range v.entries {
		if filepath.Dir(cachedPath) == path {
			if _, ok := hideNames[cachedEntry.Name]; !ok && !isAppleMetadataName(cachedEntry.Name) {
				hideNames[cachedEntry.Name] = time.Now().Add(directoryCopyHideTTL)
			}
			delete(v.entries, cachedPath)
		}
	}
	v.invalidateListLocked(path)
	v.mu.Unlock()
	v.setCopyHidden(path, hideNames)
	return nil
}

func (v *VFS) withPendingChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	parentPath = cleanVirtual(parentPath)
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.Name] = true
	}
	for _, pending := range v.cache.Pending() {
		if filepath.Dir(pending.Path) != parentPath || seen[pending.Name] || v.isDeleted(pending.Path) {
			continue
		}
		entries = append(entries, drive.Entry{
			ID:       pending.FID,
			ParentID: pending.ParentID,
			Name:     pending.Name,
			Size:     pending.Size,
		})
		seen[pending.Name] = true
	}
	return entries
}

func (v *VFS) Mkdir(ctx context.Context, path string) (drive.Entry, error) {
	if v.writer == nil {
		return drive.Entry{}, fmt.Errorf("vfs: driver does not support mkdir")
	}
	path = cleanVirtual(path)
	if entry, err := v.resolve(ctx, path); err == nil {
		if entry.IsDir {
			return entry, nil
		}
		return drive.Entry{}, fmt.Errorf("vfs: %s exists and is not a directory", path)
	}
	if entry, ok := v.restoreDeletedPath(path); ok {
		return entry, nil
	}
	v.restoreDeletedAncestor(filepath.Dir(path))
	if v.isUnderRestoredDir(path) {
		if entry, err := v.resolve(ctx, path); err == nil && entry.IsDir {
			return entry, nil
		}
	}
	parent, name, err := v.parent(ctx, path)
	if err != nil {
		return drive.Entry{}, err
	}
	entry, err := v.writer.Mkdir(ctx, parent.ID, name)
	if err != nil {
		if !isAlreadyExistsError(err) {
			return drive.Entry{}, err
		}
		entry, err = v.findExistingChildDir(ctx, filepath.Dir(path), parent.ID, name)
		if err != nil {
			return drive.Entry{}, err
		}
	}
	v.mu.Lock()
	v.entries[path] = entry
	v.invalidateListLocked(filepath.Dir(path))
	v.mu.Unlock()
	return entry, nil
}

func (v *VFS) findExistingChildDir(ctx context.Context, parentPath, parentID, name string) (drive.Entry, error) {
	entries, err := v.driver.List(ctx, parentID)
	if err != nil {
		return drive.Entry{}, err
	}
	v.mu.Lock()
	for _, child := range entries {
		childPath := joinVirtual(parentPath, child.Name)
		v.entries[childPath] = child
		if child.Name == name && child.IsDir {
			v.mu.Unlock()
			return child, nil
		}
	}
	v.mu.Unlock()
	return drive.Entry{}, fmt.Errorf("vfs: existing directory not found: %s", joinVirtual(parentPath, name))
}

func (v *VFS) Remove(ctx context.Context, path string) error {
	if v.writer == nil {
		return fmt.Errorf("vfs: driver does not support remove")
	}
	path = cleanVirtual(path)
	if _, err := v.pending(path); err == nil {
		v.cancelUpload(path)
		return v.cache.RemovePending(path)
	}
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	v.markDeleted(path, entry)
	v.scheduleDelete(path, entry)
	return nil
}

func (v *VFS) RemoveDir(ctx context.Context, path string) error {
	if v.writer == nil {
		return fmt.Errorf("vfs: driver does not support remove")
	}
	path = cleanVirtual(path)
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	if !entry.IsDir {
		return fmt.Errorf("vfs: %s is not a directory", path)
	}
	v.cancelChildUploads(path)
	if err := v.cache.RemovePendingUnder(path); err != nil {
		return err
	}
	v.cancelChildDeletes(path)
	v.markDeleted(path, entry)
	v.scheduleDelete(path, entry)
	return nil
}

func (v *VFS) Rename(ctx context.Context, oldPath, newPath string) error {
	if v.writer == nil {
		return fmt.Errorf("vfs: driver does not support rename")
	}
	oldPath = cleanVirtual(oldPath)
	newPath = cleanVirtual(newPath)
	if oldPath == "/" || newPath == "/" {
		return fmt.Errorf("vfs: cannot rename root")
	}

	if pending, err := v.pending(oldPath); err == nil {
		parent, name, err := v.parent(ctx, newPath)
		if err != nil {
			return err
		}
		pending.Path = newPath
		pending.ParentID = parent.ID
		pending.Name = name
		return v.cache.RenamePending(oldPath, pending)
	}

	entry, err := v.resolve(ctx, oldPath)
	if err != nil {
		return err
	}
	dstParent, newName, err := v.parent(ctx, newPath)
	if err != nil {
		return err
	}
	oldParent := filepath.Dir(oldPath)
	newParent := filepath.Dir(newPath)
	if filepath.Base(oldPath) != newName {
		if err := v.writer.Rename(ctx, entry, newName); err != nil {
			return err
		}
		entry.Name = newName
	}
	if oldParent != newParent {
		if err := v.writer.Move(ctx, entry, dstParent.ID); err != nil {
			return err
		}
		entry.ParentID = dstParent.ID
	}
	v.mu.Lock()
	delete(v.entries, oldPath)
	delete(v.entries, newPath)
	v.rebaseCachedPathsLocked(oldPath, newPath)
	v.invalidateListLocked(oldParent)
	v.invalidateListLocked(newParent)
	entry.Name = newName
	entry.ParentID = dstParent.ID
	v.entries[newPath] = entry
	v.mu.Unlock()
	v.addOverlay(oldPath, newPath, entry.ID, entry.IsDir)
	return nil
}

func (v *VFS) Truncate(ctx context.Context, path string, size int64) error {
	if size < 0 {
		return fmt.Errorf("vfs: truncate size must be non-negative")
	}
	path = cleanVirtual(path)
	pending, err := v.pending(path)
	if err != nil {
		if err := v.stageExisting(ctx, path); err != nil {
			return err
		}
		pending, err = v.pending(path)
		if err != nil {
			return err
		}
	}
	if err := v.cache.staging.truncate(pending.LocalPath, size); err != nil {
		return err
	}
	pending.Size = size
	return v.cache.SavePending(pending)
}

func (v *VFS) stageExisting(ctx context.Context, path string) error {
	parent, name, err := v.parent(ctx, path)
	if err != nil {
		return err
	}
	fid := stagingFID(path)
	localPath, err := v.cache.staging.create(fid)
	if err != nil {
		return err
	}
	if entry, err := v.resolve(ctx, path); err == nil && !entry.IsDir {
		rc, err := v.driver.Read(ctx, entry, 0, 0)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(f, rc)
		closeErr := f.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	size, _ := v.cache.staging.size(localPath)
	return v.cache.SavePending(PendingFile{
		Path:      path,
		FID:       fid,
		ParentID:  parent.ID,
		Name:      name,
		LocalPath: localPath,
		Size:      size,
	})
}

func (v *VFS) Pending() []PendingFile {
	return v.cache.Pending()
}

func (v *VFS) uploadWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			v.stopUploadTimers()
			v.stopDeleteTimers()
			return
		case pending := <-v.queue:
			_ = v.uploadPending(ctx, pending)
		}
	}
}

func (v *VFS) uploadPending(ctx context.Context, pending PendingFile) error {
	if v.upload == nil {
		return fmt.Errorf("vfs: driver does not support upload")
	}
	latest, ok := v.cache.PendingByPath(pending.Path)
	if !ok {
		return nil
	}
	if !samePendingFile(latest, pending) {
		v.enqueue(latest)
		return nil
	}
	rc, err := v.cache.staging.open(pending.LocalPath)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := v.removeExistingFile(ctx, pending.ParentID, pending.Name); err != nil {
		return err
	}
	entry, err := v.upload.Put(ctx, pending.ParentID, pending.Name, pending.Size, rc)
	if err != nil {
		if ctx.Err() == nil {
			if latest, ok := v.cache.PendingByPath(pending.Path); ok {
				v.enqueue(latest)
			}
		}
		return err
	}
	removed, err := v.cache.RemovePendingIfUnchanged(pending)
	if err != nil {
		return err
	}
	if !removed {
		if v.writer != nil && ctx.Err() == nil {
			_ = v.writer.Remove(context.WithoutCancel(ctx), entry)
		}
		if latest, ok := v.cache.PendingByPath(pending.Path); ok {
			v.enqueue(latest)
		}
		return nil
	}
	v.mu.Lock()
	v.entries[pending.Path] = entry
	v.unhideCopyChild(filepath.Dir(pending.Path), pending.Name)
	v.invalidateListLocked(filepath.Dir(pending.Path))
	v.mu.Unlock()
	return nil
}

func (v *VFS) removeExistingFile(ctx context.Context, parentID, name string) error {
	if v.writer == nil {
		return nil
	}
	entries, err := v.driver.List(ctx, parentID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name == name && !entry.IsDir {
			if err := v.writer.Remove(ctx, entry); err != nil {
				return err
			}
			return nil
		}
	}
	return nil
}

func (v *VFS) enqueue(p PendingFile) {
	if v.uploadDelay > 0 {
		v.scheduleUpload(p)
		return
	}
	v.sendUpload(p)
}

func (v *VFS) scheduleUpload(p PendingFile) {
	v.uploadMu.Lock()
	if timer := v.uploadTimers[p.Path]; timer != nil {
		timer.Stop()
	}
	v.uploadTimers[p.Path] = time.AfterFunc(v.uploadDelay, func() {
		v.uploadMu.Lock()
		delete(v.uploadTimers, p.Path)
		v.uploadMu.Unlock()
		v.sendUpload(p)
	})
	v.uploadMu.Unlock()
}

func (v *VFS) cancelUpload(path string) {
	path = cleanVirtual(path)
	v.uploadMu.Lock()
	if timer := v.uploadTimers[path]; timer != nil {
		timer.Stop()
		delete(v.uploadTimers, path)
	}
	v.uploadMu.Unlock()
}

func (v *VFS) cancelChildUploads(dir string) {
	dir = cleanVirtual(dir)
	v.uploadMu.Lock()
	for path, timer := range v.uploadTimers {
		if path == dir || isPathUnder(path, dir) {
			timer.Stop()
			delete(v.uploadTimers, path)
		}
	}
	v.uploadMu.Unlock()
}

func (v *VFS) sendUpload(p PendingFile) {
	select {
	case v.queue <- p:
	default:
		go func() { v.queue <- p }()
	}
}

func (v *VFS) markDeleted(path string, entry drive.Entry) {
	v.deleteMu.Lock()
	v.deleted[path] = entry
	delete(v.overlayOps, path)
	delete(v.restoredDirs, path)
	v.deleteMu.Unlock()

	v.mu.Lock()
	delete(v.entries, path)
	v.invalidateListLocked(filepath.Dir(path))
	if entry.IsDir {
		for cachedPath := range v.entries {
			if isPathUnder(cachedPath, path) {
				delete(v.entries, cachedPath)
			}
		}
		for cachedPath := range v.lists {
			if cachedPath == path || isPathUnder(cachedPath, path) {
				delete(v.lists, cachedPath)
			}
		}
	}
	v.mu.Unlock()
}

func (v *VFS) restoreDeletedPath(path string) (drive.Entry, bool) {
	path = cleanVirtual(path)
	v.deleteMu.Lock()
	entry, ok := v.deleted[path]
	if !ok {
		v.deleteMu.Unlock()
		return drive.Entry{}, false
	}
	delete(v.deleted, path)
	if timer := v.deleteTimers[path]; timer != nil {
		timer.Stop()
		delete(v.deleteTimers, path)
	}
	if entry.IsDir {
		v.restoredDirs[path] = time.Now().Add(restoredDirTTL)
	}
	v.deleteMu.Unlock()

	v.mu.Lock()
	v.entries[path] = entry
	v.invalidateListLocked(filepath.Dir(path))
	v.mu.Unlock()
	return entry, true
}

func (v *VFS) restoreDeletedAncestor(path string) {
	path = cleanVirtual(path)
	v.deleteMu.Lock()
	var restorePath string
	var entry drive.Entry
	for deletedPath, deletedEntry := range v.deleted {
		if deletedEntry.IsDir && (path == deletedPath || isPathUnder(path, deletedPath)) {
			if restorePath == "" || len(deletedPath) > len(restorePath) {
				restorePath = deletedPath
				entry = deletedEntry
			}
		}
	}
	if restorePath == "" {
		v.deleteMu.Unlock()
		return
	}
	delete(v.deleted, restorePath)
	if timer := v.deleteTimers[restorePath]; timer != nil {
		timer.Stop()
		delete(v.deleteTimers, restorePath)
	}
	v.restoredDirs[restorePath] = time.Now().Add(restoredDirTTL)
	v.deleteMu.Unlock()

	v.mu.Lock()
	v.entries[restorePath] = entry
	v.invalidateListLocked(filepath.Dir(restorePath))
	v.mu.Unlock()
}

func (v *VFS) cancelDeletedFile(path string) {
	path = cleanVirtual(path)
	v.deleteMu.Lock()
	entry, ok := v.deleted[path]
	if ok && !entry.IsDir {
		delete(v.deleted, path)
		if timer := v.deleteTimers[path]; timer != nil {
			timer.Stop()
			delete(v.deleteTimers, path)
		}
	}
	v.deleteMu.Unlock()
}

func (v *VFS) addOverlay(oldPath, newPath, entryID string, recursive bool) {
	oldPath = cleanVirtual(oldPath)
	newPath = cleanVirtual(newPath)
	v.deleteMu.Lock()
	v.overlayOps[oldPath] = overlayOp{oldPath: oldPath, newPath: newPath, entryID: entryID, isDir: recursive}
	if recursive {
		for key, op := range v.overlayOps {
			if key != oldPath && isPathUnder(op.oldPath, oldPath) {
				delete(v.overlayOps, key)
			}
		}
	}
	v.deleteMu.Unlock()
}

func (v *VFS) scheduleDelete(path string, entry drive.Entry) {
	if v.deleteDelay <= 0 {
		v.deleteRemote(context.Background(), path, entry)
		return
	}
	v.deleteMu.Lock()
	if timer := v.deleteTimers[path]; timer != nil {
		timer.Stop()
	}
	v.deleteTimers[path] = time.AfterFunc(v.deleteDelay, func() {
		v.deleteMu.Lock()
		delete(v.deleteTimers, path)
		v.deleteMu.Unlock()
		v.deleteRemote(context.Background(), path, entry)
	})
	v.deleteMu.Unlock()
}

func (v *VFS) cancelChildDeletes(dir string) {
	dir = cleanVirtual(dir)
	v.deleteMu.Lock()
	for path, timer := range v.deleteTimers {
		if isPathUnder(path, dir) {
			timer.Stop()
			delete(v.deleteTimers, path)
			delete(v.deleted, path)
		}
	}
	v.deleteMu.Unlock()
}

func (v *VFS) deleteRemote(ctx context.Context, path string, entry drive.Entry) {
	v.deleteMu.Lock()
	current, ok := v.deleted[path]
	if !ok || current.ID != entry.ID {
		v.deleteMu.Unlock()
		return
	}
	v.deleteMu.Unlock()
	if err := v.writer.Remove(ctx, entry); err != nil {
		return
	}
	v.deleteMu.Lock()
	delete(v.deleted, path)
	delete(v.restoredDirs, path)
	v.deleteMu.Unlock()
	_ = v.cache.RemovePending(path)
}

func (v *VFS) stopUploadTimers() {
	v.uploadMu.Lock()
	defer v.uploadMu.Unlock()
	for path, timer := range v.uploadTimers {
		timer.Stop()
		delete(v.uploadTimers, path)
	}
}

func (v *VFS) stopDeleteTimers() {
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	for path, timer := range v.deleteTimers {
		timer.Stop()
		delete(v.deleteTimers, path)
	}
}

func (v *VFS) pending(path string) (PendingFile, error) {
	path = cleanVirtual(path)
	for _, p := range v.cache.Pending() {
		if p.Path == path {
			return p, nil
		}
	}
	return PendingFile{}, fmt.Errorf("vfs: no pending file for %s", path)
}

func (v *VFS) rebaseCachedPathsLocked(oldPath, newPath string) {
	oldPath = cleanVirtual(oldPath)
	newPath = cleanVirtual(newPath)
	for path, entry := range v.entries {
		if !isPathUnder(path, oldPath) {
			continue
		}
		nextPath := joinVirtual(newPath, strings.TrimPrefix(path, oldPath+"/"))
		delete(v.entries, path)
		v.entries[nextPath] = entry
	}
}

func (v *VFS) parent(ctx context.Context, path string) (drive.Entry, string, error) {
	path = cleanVirtual(path)
	name := filepath.Base(path)
	parentPath := filepath.Dir(path)
	parent, err := v.resolve(ctx, parentPath)
	return parent, name, err
}

func (v *VFS) resolve(ctx context.Context, path string) (drive.Entry, error) {
	path = cleanVirtual(path)
	if v.isUnavailable(path) {
		return drive.Entry{}, fmt.Errorf("vfs: not found: %s", path)
	}
	v.mu.RLock()
	entry, ok := v.entries[path]
	v.mu.RUnlock()
	if ok {
		return entry, nil
	}
	parentPath := filepath.Dir(path)
	name := filepath.Base(path)
	parent, err := v.resolve(ctx, parentPath)
	if err != nil {
		return drive.Entry{}, err
	}
	entries, err := v.listChildren(ctx, parentPath, parent.ID)
	if err != nil {
		return drive.Entry{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, child := range entries {
		childPath := joinVirtual(parentPath, child.Name)
		v.entries[childPath] = child
		if child.Name == name {
			return child, nil
		}
	}
	return drive.Entry{}, fmt.Errorf("vfs: not found: %s", path)
}

type listCacheEntry struct {
	entries []drive.Entry
	expires time.Time
}

func (v *VFS) listChildren(ctx context.Context, parentPath, parentID string) ([]drive.Entry, error) {
	parentPath = cleanVirtual(parentPath)
	now := time.Now()
	v.mu.RLock()
	cached, ok := v.lists[parentPath]
	if ok && now.Before(cached.expires) {
		entries := cloneEntries(cached.entries)
		v.mu.RUnlock()
		return v.localChildren(parentPath, v.filterDeleted(parentPath, entries)), nil
	}
	v.mu.RUnlock()

	entries, err := v.driver.List(ctx, parentID)
	if err != nil {
		return nil, err
	}
	v.updateOverlay(parentPath, entries)
	entries = v.filterDeleted(parentPath, entries)
	v.mu.Lock()
	for _, child := range entries {
		v.entries[joinVirtual(parentPath, child.Name)] = child
	}
	v.lists[parentPath] = listCacheEntry{entries: cloneEntries(entries), expires: now.Add(listCacheTTL)}
	v.mu.Unlock()
	return v.localChildren(parentPath, entries), nil
}

func (v *VFS) invalidateListLocked(path string) {
	delete(v.lists, cleanVirtual(path))
}

func (v *VFS) isUnavailable(path string) bool {
	return v.isDeleted(path) || v.isHidden(path) || v.isCopyHidden(path)
}

func (v *VFS) isDeleted(path string) bool {
	path = cleanVirtual(path)
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	for deletedPath, entry := range v.deleted {
		if path == deletedPath || (entry.IsDir && isPathUnder(path, deletedPath)) {
			return true
		}
	}
	return false
}

func (v *VFS) isHidden(path string) bool {
	path = cleanVirtual(path)
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	for _, op := range v.overlayOps {
		if path == op.oldPath || (op.isDir && isPathUnder(path, op.oldPath)) {
			return true
		}
	}
	return false
}

func (v *VFS) isUnderRestoredDir(path string) bool {
	path = cleanVirtual(path)
	now := time.Now()
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	for restoredPath, expires := range v.restoredDirs {
		if now.After(expires) {
			delete(v.restoredDirs, restoredPath)
			continue
		}
		if path == restoredPath || isPathUnder(path, restoredPath) {
			return true
		}
	}
	return false
}

func (v *VFS) setCopyHidden(dir string, names map[string]time.Time) {
	dir = cleanVirtual(dir)
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	if len(names) == 0 {
		delete(v.copyHidden, dir)
		return
	}
	v.copyHidden[dir] = names
}

func (v *VFS) unhideCopyChild(parentPath, name string) {
	parentPath = cleanVirtual(parentPath)
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	if names := v.copyHidden[parentPath]; names != nil {
		delete(names, name)
		if len(names) == 0 {
			delete(v.copyHidden, parentPath)
		}
	}
}

func (v *VFS) isCopyHidden(path string) bool {
	path = cleanVirtual(path)
	parentPath := filepath.Dir(path)
	name := filepath.Base(path)
	now := time.Now()
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	names := v.copyHidden[parentPath]
	if len(names) == 0 {
		delete(v.copyHidden, parentPath)
		return false
	}
	expires, ok := names[name]
	if !ok {
		return false
	}
	if now.After(expires) {
		delete(names, name)
		if len(names) == 0 {
			delete(v.copyHidden, parentPath)
		}
		return false
	}
	return true
}

func (v *VFS) updateOverlay(parentPath string, entries []drive.Entry) {
	parentPath = cleanVirtual(parentPath)
	v.deleteMu.Lock()
	defer v.deleteMu.Unlock()
	for key, op := range v.overlayOps {
		if filepath.Dir(op.oldPath) == parentPath {
			op.oldGone = !entryListHasPath(entries, filepath.Base(op.oldPath), op.entryID)
		}
		if filepath.Dir(op.newPath) == parentPath {
			op.newSeen = entryListHasPath(entries, filepath.Base(op.newPath), op.entryID)
		}
		if op.oldGone && op.newSeen {
			delete(v.overlayOps, key)
			continue
		}
		v.overlayOps[key] = op
	}
}

func (v *VFS) filterDeleted(parentPath string, entries []drive.Entry) []drive.Entry {
	entries = cloneEntries(entries)
	filtered := entries[:0]
	for _, entry := range entries {
		if v.isUnavailable(joinVirtual(parentPath, entry.Name)) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func (v *VFS) localChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	parentPath = cleanVirtual(parentPath)
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.Name] = true
	}
	var local []struct {
		path  string
		entry drive.Entry
	}
	v.mu.RLock()
	for path, entry := range v.entries {
		if path == "/" || filepath.Dir(path) != parentPath || seen[entry.Name] {
			continue
		}
		local = append(local, struct {
			path  string
			entry drive.Entry
		}{path: path, entry: entry})
	}
	v.mu.RUnlock()
	for _, item := range local {
		if seen[item.entry.Name] || v.isUnavailable(item.path) {
			continue
		}
		entries = append(entries, item.entry)
		seen[item.entry.Name] = true
	}
	return entries
}

func cloneEntries(entries []drive.Entry) []drive.Entry {
	if entries == nil {
		return nil
	}
	cloned := make([]drive.Entry, len(entries))
	copy(cloned, entries)
	return cloned
}

func cleanVirtual(path string) string {
	path = filepath.ToSlash(filepath.Clean("/" + strings.TrimPrefix(path, "/")))
	if path == "." {
		return "/"
	}
	return path
}

func joinVirtual(parent, name string) string {
	parent = cleanVirtual(parent)
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

func isPathUnder(path, dir string) bool {
	path = cleanVirtual(path)
	dir = cleanVirtual(dir)
	return dir != "/" && strings.HasPrefix(path, dir+"/")
}

func isAppleMetadataFile(path string) bool {
	return isAppleMetadataName(filepath.Base(cleanVirtual(path)))
}

func isAppleMetadataName(name string) bool {
	return name == ".DS_Store" || strings.HasPrefix(name, "._")
}

func isAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "already exists") ||
		strings.Contains(text, "file exists") ||
		strings.Contains(text, "同名冲突") ||
		strings.Contains(text, "已存在")
}

func entryListHasPath(entries []drive.Entry, name, entryID string) bool {
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}
		if entryID == "" || entry.ID == "" || entry.ID == entryID {
			return true
		}
	}
	return false
}

func stagingFID(path string) string {
	path = strings.Trim(cleanVirtual(path), "/")
	if path == "" {
		return "root"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_")
	return replacer.Replace(path)
}
