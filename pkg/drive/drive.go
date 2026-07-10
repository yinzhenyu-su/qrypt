package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// Entry describes one object on a backend.
type Entry struct {
	ID       string    `json:"id"`
	ParentID string    `json:"parent_id,omitempty"`
	Name     string    `json:"name"`
	IsDir    bool      `json:"is_dir"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"mod_time,omitempty"`
	Extra    any       `json:"-"`
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

// ReadOnlyFile is a stable, seekable, read-only file handle.
type ReadOnlyFile interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
}

// ReadOnlyFileSource opens stable, read-only handles over one upload source.
// Implementations may return a fresh handle on each Open call. Callers must
// close every opened handle.
type ReadOnlyFileSource interface {
	Size() int64
	Open(ctx context.Context) (ReadOnlyFile, error)
}

type HashAlgorithm string

const (
	HashMD5    HashAlgorithm = "md5"
	HashSHA1   HashAlgorithm = "sha1"
	HashSHA256 HashAlgorithm = "sha256"
)

type SourceHashes map[HashAlgorithm][]byte

// HashProvider is an optional source metadata interface for callers that
// already computed content hashes while preparing the source.
type HashProvider interface {
	Hash(algorithm HashAlgorithm) ([]byte, bool)
}

// UploadHashRequirements lets a driver request source hashes that should be
// available before PutSource starts streaming upload data.
type UploadHashRequirements interface {
	RequiredUploadHashes() []HashAlgorithm
}

type UploadPhase string

const (
	UploadPhasePreparing  UploadPhase = "preparing"
	UploadPhaseHashing    UploadPhase = "hashing"
	UploadPhaseUploading  UploadPhase = "uploading"
	UploadPhaseInstant    UploadPhase = "instant"
	UploadPhaseCommitting UploadPhase = "committing"
	UploadPhaseCompleted  UploadPhase = "completed"
)

// UploadProgress receives progress for the logical upload represented by an
// UploadRequest. Implementations must be safe to call repeatedly with small
// positive byte deltas.
type UploadProgress interface {
	Phase(UploadPhase)
	Uploaded(n int64)
}

type UploadRequest struct {
	ParentID string
	Name     string
	Source   ReadOnlyFileSource
	Progress UploadProgress
}

func SourceHash(source ReadOnlyFileSource, algorithm HashAlgorithm) ([]byte, bool) {
	provider, ok := source.(HashProvider)
	if !ok {
		return nil, false
	}
	sum, ok := provider.Hash(algorithm)
	if !ok {
		return nil, false
	}
	return append([]byte(nil), sum...), true
}

// SourceUploader is an optional upload fast path for drivers that can benefit
// from a stable source that supports repeated reads, seeking, and ReadAt.
type SourceUploader interface {
	PutSource(ctx context.Context, req UploadRequest) (Entry, error)
}

// Space describes backend capacity in bytes.
type Space struct {
	Total int64
	Free  int64
}

var ErrSpaceUnsupported = errors.New("drive: space query unsupported")

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

// ParamDef describes a single configuration parameter for a driver.
// Each driver should declare its expected params via Register so the CLI
// can provide meaningful validation, help output, and documentation.
type ParamDef struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"` // "string" (default), "int", "bool", "duration"
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"` // masked in debug output and help
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
	Example     string `json:"example,omitempty"`
}

var (
	registryMu   sync.RWMutex
	registry     = map[string]Factory{}
	paramSchemas = map[string][]ParamDef{}
)

// Register makes a backend driver available by name with optional parameter
// schema. The schema enables config validation and generated documentation.
func Register(name string, factory Factory, schema ...ParamDef) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = factory
	paramSchemas[name] = schema
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

// ParamSchema returns the parameter schema for a registered driver.
func ParamSchema(name string) []ParamDef {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return paramSchemas[name]
}

// Registered reports whether name identifies an available driver.
func Registered(name string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[name] != nil
}

// Names returns registered driver names in alphabetical order.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
