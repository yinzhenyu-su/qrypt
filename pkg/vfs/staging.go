package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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

func fidFromStagingPath(path string) string {
	base := filepath.Base(path)
	if filepath.Ext(base) == ".staging" {
		return base[:len(base)-len(".staging")]
	}
	return base
}
