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
	"strings"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const readChunkSize = 64 * 1024

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
	queue   chan PendingFile
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
		driver:  driver,
		cache:   cache,
		rootID:  opts.RootID,
		entries: map[string]drive.Entry{},
		queue:   make(chan PendingFile, 128),
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
	entries, err := v.driver.List(ctx, entry.ID)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	for _, child := range entries {
		v.entries[joinVirtual(path, child.Name)] = child
	}
	v.mu.Unlock()
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
	startChunk := offset / readChunkSize
	if cached, ok, err := v.cache.GetChunk(entry.ID, startChunk); err != nil {
		return nil, err
	} else if ok {
		start := offset % readChunkSize
		end := int64(len(cached))
		if size > 0 && start+size < end {
			end = start + size
		}
		return io.NopCloser(bytes.NewReader(cached[start:end])), nil
	}
	rc, err := v.driver.Read(ctx, entry, startChunk*readChunkSize, readChunkSize)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return nil, err
	}
	if len(data) > 0 {
		_ = v.cache.PutChunk(entry.ID, startChunk, data)
	}
	start := offset % readChunkSize
	if start > int64(len(data)) {
		start = int64(len(data))
	}
	end := int64(len(data))
	if size > 0 && start+size < end {
		end = start + size
	}
	return io.NopCloser(bytes.NewReader(data[start:end])), nil
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
	v.mu.Lock()
	delete(v.entries, cleanVirtual(path))
	v.mu.Unlock()
	return v.cache.RemovePending(cleanVirtual(path))
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
	entries, err := v.driver.List(ctx, parent.ID)
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
