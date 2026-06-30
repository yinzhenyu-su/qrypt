package drive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type StateStore interface {
	LoadJSON(name string, out any) error
	SaveJSON(name string, value any) error
}

type StateStoreInstaller interface {
	InstallStateStore(store StateStore)
}

type FileStateStore struct {
	dir string
}

func NewFileStateStore(dir string) *FileStateStore {
	return &FileStateStore{dir: dir}
}

func (s *FileStateStore) LoadJSON(name string, out any) error {
	if s == nil || s.dir == "" {
		return os.ErrNotExist
	}
	data, err := os.ReadFile(s.path(name))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (s *FileStateStore) SaveJSON(name string, value any) error {
	if s == nil || s.dir == "" {
		return fmt.Errorf("drive: state store dir is empty")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := s.path(name)
	tmp, err := os.CreateTemp(s.dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func (s *FileStateStore) path(name string) string {
	return filepath.Join(s.dir, filepath.Base(filepath.Clean(name)))
}

func IsStateNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
