package vfs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	pageFlushDelay     = 250 * time.Millisecond
	pageMaxSize        = 1 << 20
	pageInitialBufSize = 4096
)

type stagingStore struct {
	dir   string
	pages sync.Map
}

type page struct {
	mu        sync.Mutex
	fid       string
	buf       []byte
	dirty     bool
	loaded    bool
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

func (s *stagingStore) readAt(path string, buf []byte, off int64) (int, error) {
	if p, ok := s.pages.Load(fidFromStagingPath(path)); ok {
		pg := p.(*page)
		pg.mu.Lock()
		if off < pg.maxOffset {
			n := copy(buf, pg.buf[off:min(pg.maxOffset, off+int64(len(buf)))])
			pg.mu.Unlock()
			return n, nil
		}
		pg.mu.Unlock()
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.ReadAt(buf, off)
}

func (s *stagingStore) open(path string) (io.ReadCloser, error) {
	if err := s.flush(path); err != nil {
		return nil, err
	}
	return os.Open(path)
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

func (s *stagingStore) getPage(fid string) *page {
	if p, ok := s.pages.Load(fid); ok {
		return p.(*page)
	}
	p := &page{
		fid: fid,
		buf: make([]byte, 0, pageInitialBufSize),
		flush: func(fid string, data []byte) error {
			return writeFileSync(s.path(fid), data, 0o644)
		},
	}
	actual, _ := s.pages.LoadOrStore(fid, p)
	return actual.(*page)
}

func (p *page) writeAt(path string, data []byte, off int64) (int, error) {
	p.mu.Lock()
	if !p.loaded {
		existing, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			p.mu.Unlock()
			return 0, err
		}
		p.buf = append(p.buf[:0], existing...)
		p.maxOffset = int64(len(existing))
		p.loaded = true
	}
	need := off + int64(len(data))
	if need > int64(len(p.buf)) {
		newSize := roundUpPow2(need)
		if newSize > pageMaxSize {
			newSize = need
		}
		next := make([]byte, newSize)
		copy(next, p.buf)
		p.buf = next
	}
	copy(p.buf[off:], data)
	p.dirty = true
	if need > p.maxOffset {
		p.maxOffset = need
	}
	p.resetTimerLocked()
	p.mu.Unlock()
	return len(data), nil
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

func (p *page) resetTimerLocked() {
	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = time.AfterFunc(pageFlushDelay, func() {
		_ = p.flushNow()
	})
}

func fidFromStagingPath(path string) string {
	base := filepath.Base(path)
	if filepath.Ext(base) == ".staging" {
		return base[:len(base)-len(".staging")]
	}
	return base
}

func roundUpPow2(v int64) int64 {
	if v <= 0 {
		return 0
	}
	v--
	for i := 1; i < 64; i <<= 1 {
		v |= v >> i
	}
	return v + 1
}
