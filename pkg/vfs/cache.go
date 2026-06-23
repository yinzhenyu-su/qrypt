package vfs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const cacheBatchBlocks = 16

type PendingFile struct {
	Path      string `json:"path"`
	FID       string `json:"fid"`
	ParentID  string `json:"parent_id"`
	Name      string `json:"name"`
	LocalPath string `json:"local_path"`
	Size      int64  `json:"size"`
	UpdatedAt int64  `json:"updated_at"`
}

type journalEntry struct {
	Op string `json:"op"`
	PendingFile
}

type chunkInfo struct {
	file     string
	offset   int64
	size     int64
	accessAt time.Time
}

type fileChunks struct {
	mu     sync.RWMutex
	chunks map[int64]chunkInfo
}

type Cache struct {
	dir     string
	maxSize int64
	staging *stagingStore

	mu      sync.RWMutex
	pending map[string]PendingFile
	chunks  map[string]*fileChunks
}

func NewCache(dir string, maxSize int64) (*Cache, error) {
	if err := os.MkdirAll(filepath.Join(dir, "reading"), 0o755); err != nil {
		return nil, err
	}
	staging, err := newStagingStore(filepath.Join(dir, "staging"))
	if err != nil {
		return nil, err
	}
	c := &Cache{
		dir:     dir,
		maxSize: maxSize,
		staging: staging,
		pending: map[string]PendingFile{},
		chunks:  map[string]*fileChunks{},
	}
	if err := c.loadJournal(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Cache) Pending() []PendingFile {
	c.mu.RLock()
	defer c.mu.RUnlock()
	files := make([]PendingFile, 0, len(c.pending))
	for _, pending := range c.pending {
		files = append(files, pending)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func (c *Cache) SavePending(p PendingFile) error {
	p.UpdatedAt = time.Now().UnixNano()
	c.mu.Lock()
	c.pending[p.Path] = p
	c.mu.Unlock()
	return c.appendJournal(journalEntry{Op: "dirty", PendingFile: p})
}

func (c *Cache) RemovePending(path string) error {
	c.mu.Lock()
	pending, ok := c.pending[path]
	delete(c.pending, path)
	c.mu.Unlock()
	if ok {
		_ = c.staging.remove(pending.LocalPath)
	}
	if err := c.appendJournal(journalEntry{Op: "clean", PendingFile: PendingFile{Path: path}}); err != nil {
		return err
	}
	return c.compactJournal()
}

func (c *Cache) RenamePending(oldPath string, next PendingFile) error {
	c.mu.Lock()
	delete(c.pending, oldPath)
	c.pending[next.Path] = next
	c.mu.Unlock()
	if err := c.appendJournal(journalEntry{Op: "clean", PendingFile: PendingFile{Path: oldPath}}); err != nil {
		return err
	}
	if err := c.appendJournal(journalEntry{Op: "dirty", PendingFile: next}); err != nil {
		return err
	}
	return c.compactJournal()
}

func (c *Cache) GetChunk(fid string, index int64) ([]byte, bool, error) {
	fc := c.fileChunks(fid)
	fc.mu.RLock()
	info, ok := fc.chunks[index]
	fc.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	f, err := os.Open(info.file)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data := make([]byte, info.size)
	if _, err := f.ReadAt(data, info.offset); err != nil {
		return nil, false, err
	}
	info.accessAt = time.Now()
	fc.mu.Lock()
	fc.chunks[index] = info
	fc.mu.Unlock()
	return data, true, nil
}

func (c *Cache) PutChunk(fid string, index int64, data []byte) error {
	batch := index / cacheBatchBlocks
	offset := int64(index%cacheBatchBlocks) * int64(len(data))
	path := filepath.Join(c.dir, "reading", fmt.Sprintf("%s_%d.batch", fid, batch))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt(data, offset); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fc := c.fileChunks(fid)
	fc.mu.Lock()
	fc.chunks[index] = chunkInfo{file: path, offset: offset, size: int64(len(data)), accessAt: time.Now()}
	fc.mu.Unlock()
	return c.evictIfNeeded()
}

func (c *Cache) fileChunks(fid string) *fileChunks {
	c.mu.Lock()
	defer c.mu.Unlock()
	fc := c.chunks[fid]
	if fc == nil {
		fc = &fileChunks{chunks: map[int64]chunkInfo{}}
		c.chunks[fid] = fc
	}
	return fc
}

func (c *Cache) journalPath() string {
	return filepath.Join(c.dir, "pending.jsonl")
}

func (c *Cache) appendJournal(entry journalEntry) error {
	f, err := os.OpenFile(c.journalPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

func (c *Cache) loadJournal() error {
	f, err := os.Open(c.journalPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry journalEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		switch entry.Op {
		case "dirty":
			if _, err := os.Stat(entry.LocalPath); err == nil {
				c.pending[entry.Path] = entry.PendingFile
			}
		case "clean":
			delete(c.pending, entry.Path)
		}
	}
	return scanner.Err()
}

func (c *Cache) compactJournal() error {
	tmp := c.journalPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	c.mu.RLock()
	for _, p := range c.pending {
		data, err := json.Marshal(journalEntry{Op: "dirty", PendingFile: p})
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
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, c.journalPath())
}

func (c *Cache) evictIfNeeded() error {
	if c.maxSize <= 0 {
		return nil
	}
	var total int64
	var chunks []struct {
		fid string
		idx int64
		ch  chunkInfo
	}
	c.mu.RLock()
	for fid, fc := range c.chunks {
		fc.mu.RLock()
		for idx, ch := range fc.chunks {
			total += ch.size
			chunks = append(chunks, struct {
				fid string
				idx int64
				ch  chunkInfo
			}{fid: fid, idx: idx, ch: ch})
		}
		fc.mu.RUnlock()
	}
	c.mu.RUnlock()
	if total <= c.maxSize {
		return nil
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].ch.accessAt.Before(chunks[j].ch.accessAt) })
	for _, item := range chunks {
		if total <= c.maxSize*7/10 {
			break
		}
		_ = os.Remove(item.ch.file)
		fc := c.fileChunks(item.fid)
		fc.mu.Lock()
		delete(fc.chunks, item.idx)
		fc.mu.Unlock()
		total -= item.ch.size
	}
	return nil
}
