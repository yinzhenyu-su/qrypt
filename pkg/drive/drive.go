// Package drive defines the minimal backend contract used by qrypt.
package drive

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// Entry describes one object on a backend.
type Entry struct {
	ID       string
	ParentID string
	Name     string
	IsDir    bool
	Size     int64
	ModTime  time.Time
	Extra    any
}

// Driver is the read-only portion every cloud drive adapter must implement.
// Alist-style providers should be adapted to this interface rather than being
// referenced directly from the VFS layer.
type Driver interface {
	Init(ctx context.Context) error
	Drop(ctx context.Context) error
	List(ctx context.Context, parentID string) ([]Entry, error)
	Read(ctx context.Context, entry Entry, offset, size int64) (io.ReadCloser, error)
}

// Writer adds metadata mutation support.
type Writer interface {
	Mkdir(ctx context.Context, parentID, name string) (Entry, error)
	Move(ctx context.Context, entry Entry, dstParentID string) error
	Rename(ctx context.Context, entry Entry, newName string) error
	Remove(ctx context.Context, entry Entry) error
}

// Uploader adds streaming upload support.
type Uploader interface {
	Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (Entry, error)
}

// FileUploader is an optional upload fast path for drivers that can benefit
// from a stable local source file. Implementations must not mutate localPath
// and must treat the file contents as read-only for the duration of the call.
type FileUploader interface {
	PutFile(ctx context.Context, parentID, name string, size int64, localPath string) (Entry, error)
}

// Space describes backend capacity in bytes.
type Space struct {
	Total int64
	Free  int64
}

// SpaceQuerier is implemented by drivers that can report backend capacity.
type SpaceQuerier interface {
	Space(ctx context.Context) (Space, error)
}

// PathResolver lets drivers map a virtual path to their native object ID.
type PathResolver interface {
	ResolvePath(ctx context.Context, path string) (string, error)
}

// Params are driver-specific configuration values.
type Params map[string]string

// Factory constructs a backend driver from Params.
type Factory func(Params) (Driver, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register makes a backend driver available by name.
func Register(name string, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = factory
}

// New constructs a registered backend driver.
func New(name string, params Params) (Driver, error) {
	registryMu.RLock()
	factory := registry[name]
	registryMu.RUnlock()
	if factory == nil {
		return nil, fmt.Errorf("drive: unknown driver %q", name)
	}
	return factory(params)
}

// Names returns registered driver names.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}
