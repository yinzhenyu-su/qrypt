package drive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yinzhenyu/qrypt/pkg/util"
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
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := s.path(name)
	return util.WriteAtomicWithOptions(path, util.AtomicWriteOptions{
		Pattern:      filepath.Base(path) + ".tmp-*",
		Mode:         0o600,
		Replace:      true,
		CreateParent: true,
		ParentMode:   0o700,
	}, func(file *os.File) error {
		_, err := file.Write(data)
		return err
	})
}

func (s *FileStateStore) path(name string) string {
	return filepath.Join(s.dir, filepath.Base(filepath.Clean(name)))
}

func IsStateNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
