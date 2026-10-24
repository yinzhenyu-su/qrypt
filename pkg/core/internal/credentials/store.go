package credentials

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// sessionFile is the on-disk form of one credential session. Data is opaque to
// qrypt; the owning driver defines its schema.
type sessionFile struct {
	Version    uint64    `json:"version"`
	ParamsSeed string    `json:"params_seed,omitempty"`
	Account    string    `json:"account,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
	Data       []byte    `json:"data,omitempty"`
}

// fileStore keeps one session file per credential. An empty dir makes the store
// in-memory only, which keeps tests and callers without a state directory free
// of accidental persistence.
type fileStore struct {
	dir string
}

func (s fileStore) dirFor(name string) string {
	if s.dir == "" {
		return ""
	}
	return filepath.Join(s.dir, "credentials", name)
}

func (s fileStore) path(name string) string {
	dir := s.dirFor(name)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "session.json")
}

func (s fileStore) load(name string) (sessionFile, error) {
	path := s.path(name)
	if path == "" {
		return sessionFile{}, os.ErrNotExist
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return sessionFile{}, err
	}
	var file sessionFile
	if err := json.Unmarshal(data, &file); err != nil {
		return sessionFile{}, err
	}
	return file, nil
}

// save writes next when the file still carries expected, serializing writers
// from other processes with a lock next to the session file.
func (s fileStore) save(name string, next sessionFile, expected uint64) error {
	if s.dir == "" {
		return nil
	}
	release, err := s.lock(name)
	if err != nil {
		return err
	}
	defer release()
	current, err := s.load(name)
	if err == nil && current.Version != expected {
		return drive.ErrCredentialVersionConflict
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// A corrupt session must not block every future write; preserve it
		// for diagnosis and overwrite with the new value.
		if quarantineErr := s.quarantine(name); quarantineErr != nil {
			return quarantineErr
		}
	}
	return s.write(name, next)
}

// overwrite writes next without a version check. It is only used when the
// credential params changed and the previous session is invalid for everyone.
func (s fileStore) overwrite(name string, next sessionFile) error {
	if s.dir == "" {
		return nil
	}
	release, err := s.lock(name)
	if err != nil {
		return err
	}
	defer release()
	return s.write(name, next)
}

func (s fileStore) write(name string, file sessionFile) error {
	data, err := json.MarshalIndent(file, "", "  ")
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
	}, func(f *os.File) error {
		_, err := f.Write(data)
		return err
	})
}

// quarantine moves an unreadable session file aside so the next save can
// replace it.
func (s fileStore) quarantine(name string) error {
	path := s.path(name)
	if path == "" {
		return nil
	}
	if err := os.Rename(path, path+".corrupt"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s fileStore) lock(name string) (func(), error) {
	dir := s.dirFor(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, "session.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = unlockFile(file)
		_ = file.Close()
	}, nil
}
