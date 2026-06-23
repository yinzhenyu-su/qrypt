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

type Options struct {
	CacheDir      string
	CacheMaxBytes int64
	RootID        string
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

	prefetchMu  sync.Mutex
	prefetching map[string]struct{}
	prefetchSem chan struct{}

	chunkLoadMu sync.Mutex
	chunkLoads  map[string]*chunkLoad

	windowLoadMu sync.Mutex
	windowLoads  map[string]*windowLoad
}

func New(driver drive.Driver, opts Options) (*VFS, error) {
	if opts.RootID == "" {
		opts.RootID = "0"
	}
	cache, err := NewCache(opts.CacheDir, opts.CacheMaxBytes)
	if err != nil {
		return nil, err
	}
	v := &VFS{
		driver:      driver,
		cache:       cache,
		rootID:      opts.RootID,
		entries:     map[string]drive.Entry{},
		lists:       map[string]listCacheEntry{},
		queue:       make(chan PendingFile, 128),
		prefetching: map[string]struct{}{},
		prefetchSem: make(chan struct{}, readPrefetchLimit),
		chunkLoads:  map[string]*chunkLoad{},
		windowLoads: map[string]*windowLoad{},
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
	parent, name, err := v.parent(ctx, path)
	if err != nil {
		return err
	}
	fid := stagingFID(path)
	localPath, err := v.cache.staging.create(fid)
	if err != nil {
		return err
	}
	pending := PendingFile{Path: cleanVirtual(path), FID: fid, ParentID: parent.ID, Name: name, LocalPath: localPath}
	return v.cache.SavePending(pending)
}

func (v *VFS) WriteAt(ctx context.Context, path string, data []byte, off int64) (int, error) {
	pending, err := v.pending(path)
	if err != nil {
		if err := v.Create(ctx, path); err != nil {
			return 0, err
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

func (v *VFS) withPendingChildren(parentPath string, entries []drive.Entry) []drive.Entry {
	parentPath = cleanVirtual(parentPath)
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.Name] = true
	}
	for _, pending := range v.cache.Pending() {
		if filepath.Dir(pending.Path) != parentPath || seen[pending.Name] {
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
	parent, name, err := v.parent(ctx, path)
	if err != nil {
		return drive.Entry{}, err
	}
	entry, err := v.writer.Mkdir(ctx, parent.ID, name)
	if err != nil {
		return drive.Entry{}, err
	}
	v.mu.Lock()
	v.entries[cleanVirtual(path)] = entry
	v.invalidateListLocked(filepath.Dir(cleanVirtual(path)))
	v.mu.Unlock()
	return entry, nil
}

func (v *VFS) Remove(ctx context.Context, path string) error {
	if v.writer == nil {
		return fmt.Errorf("vfs: driver does not support remove")
	}
	if _, err := v.pending(path); err == nil {
		return v.cache.RemovePending(cleanVirtual(path))
	}
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	if err := v.writer.Remove(ctx, entry); err != nil {
		return err
	}
	path = cleanVirtual(path)
	v.mu.Lock()
	delete(v.entries, path)
	v.invalidateListLocked(filepath.Dir(path))
	v.mu.Unlock()
	return v.cache.RemovePending(path)
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
		entry, err = v.resolve(ctx, joinVirtual(oldParent, newName))
		if err != nil {
			entry.Name = newName
		}
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
	v.invalidateListLocked(oldParent)
	v.invalidateListLocked(newParent)
	v.mu.Unlock()
	if refreshed, err := v.resolve(ctx, newPath); err == nil {
		entry = refreshed
	}
	entry.Name = newName
	entry.ParentID = dstParent.ID
	v.mu.Lock()
	v.entries[newPath] = entry
	v.mu.Unlock()
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
	rc, err := v.cache.staging.open(pending.LocalPath)
	if err != nil {
		return err
	}
	defer rc.Close()
	entry, err := v.upload.Put(ctx, pending.ParentID, pending.Name, pending.Size, rc)
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.entries[pending.Path] = entry
	v.invalidateListLocked(filepath.Dir(pending.Path))
	v.mu.Unlock()
	return v.cache.RemovePending(pending.Path)
}

func (v *VFS) enqueue(p PendingFile) {
	select {
	case v.queue <- p:
	default:
		go func() { v.queue <- p }()
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

func (v *VFS) parent(ctx context.Context, path string) (drive.Entry, string, error) {
	path = cleanVirtual(path)
	name := filepath.Base(path)
	parentPath := filepath.Dir(path)
	parent, err := v.resolve(ctx, parentPath)
	return parent, name, err
}

func (v *VFS) resolve(ctx context.Context, path string) (drive.Entry, error) {
	path = cleanVirtual(path)
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
		return entries, nil
	}
	v.mu.RUnlock()

	entries, err := v.driver.List(ctx, parentID)
	if err != nil {
		return nil, err
	}
	entries = cloneEntries(entries)
	v.mu.Lock()
	for _, child := range entries {
		v.entries[joinVirtual(parentPath, child.Name)] = child
	}
	v.lists[parentPath] = listCacheEntry{entries: cloneEntries(entries), expires: now.Add(listCacheTTL)}
	v.mu.Unlock()
	return entries, nil
}

func (v *VFS) invalidateListLocked(path string) {
	delete(v.lists, cleanVirtual(path))
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

func stagingFID(path string) string {
	path = strings.Trim(cleanVirtual(path), "/")
	if path == "" {
		return "root"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_")
	return replacer.Replace(path)
}
