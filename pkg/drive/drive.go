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

type EntryRemoteNamer interface {
	EntryRemoteName() string
}

type EntryRawExtraer interface {
	EntryRawExtra() any
}

// EntryExtraWrapper preserves backend metadata when a higher-level driver
// transforms the public entry name, size, or other visible fields.
type EntryExtraWrapper struct {
	RemoteName string
	Raw        any
}

func (e EntryExtraWrapper) EntryRemoteName() string {
	return e.RemoteName
}

func (e EntryExtraWrapper) EntryRawExtra() any {
	return e.Raw
}

func EntryRemoteName(entry Entry) (string, bool) {
	extra, ok := entry.Extra.(EntryRemoteNamer)
	if !ok {
		return entry.Name, false
	}
	name := extra.EntryRemoteName()
	if name == "" {
		return entry.Name, false
	}
	return name, true
}

func EntryRawExtra(entry Entry) any {
	return RawEntryExtra(entry.Extra)
}

func RawEntryExtra(raw any) any {
	extra, ok := raw.(EntryRawExtraer)
	if !ok {
		return raw
	}
	next := extra.EntryRawExtra()
	if next == nil || next == raw {
		return raw
	}
	return RawEntryExtra(next)
}

// Driver is the complete operation and observability contract every cloud
// drive adapter must implement. Unsupported operations should return
// ErrUnsupported and stay absent from Capabilities.
type Driver interface {
	Init(ctx context.Context) error
	Drop(ctx context.Context) error
	List(ctx context.Context, parentID string) ([]Entry, error)
	Read(ctx context.Context, entry Entry, offset, size int64) (io.ReadCloser, error)
	Mkdir(ctx context.Context, parentID, name string) (Entry, error)
	Move(ctx context.Context, entry Entry, dstParentID string) error
	Rename(ctx context.Context, entry Entry, newName string) error
	Remove(ctx context.Context, entry Entry) error
	PutSource(ctx context.Context, req UploadRequest) (Entry, error)
	RequiredUploadHashes() []HashAlgorithm
	ResolvePath(ctx context.Context, path string) (string, error)
	ResolveRemoteName(ctx context.Context, plainName string) (RemoteNameInfo, error)
	ForeignEntries(ctx context.Context, parentID string) ([]ForeignEntry, error)
	Space(ctx context.Context) (Space, error)
	Capabilities() []Capability
	DebugSnapshot(ctx context.Context) (DebugSnapshot, error)
	Metrics(ctx context.Context, since time.Time) ([]MetricEvent, error)
}

// Space describes backend capacity in bytes.
type Space struct {
	Total int64
	Free  int64
}

var ErrUnsupported = errors.New("drive: operation unsupported")
var ErrSpaceUnsupported = errors.New("drive: space query unsupported")

// UnsupportedOperations provides default implementations for Driver methods
// that a read-only or partial driver intentionally does not advertise in
// Capabilities.
type UnsupportedOperations struct{}

func (UnsupportedOperations) Mkdir(context.Context, string, string) (Entry, error) {
	return Entry{}, ErrUnsupported
}

func (UnsupportedOperations) Move(context.Context, Entry, string) error {
	return ErrUnsupported
}

func (UnsupportedOperations) Rename(context.Context, Entry, string) error {
	return ErrUnsupported
}

func (UnsupportedOperations) Remove(context.Context, Entry) error {
	return ErrUnsupported
}

func (UnsupportedOperations) PutSource(context.Context, UploadRequest) (Entry, error) {
	return Entry{}, ErrUnsupported
}

func (UnsupportedOperations) RequiredUploadHashes() []HashAlgorithm {
	return nil
}

func (UnsupportedOperations) ResolvePath(context.Context, string) (string, error) {
	return "", ErrUnsupported
}

func (UnsupportedOperations) ResolveRemoteName(context.Context, string) (RemoteNameInfo, error) {
	return RemoteNameInfo{}, ErrUnsupported
}

func (UnsupportedOperations) ForeignEntries(context.Context, string) ([]ForeignEntry, error) {
	return nil, ErrUnsupported
}

type nonRetryableError struct {
	err error
}

func (e nonRetryableError) Error() string {
	return e.err.Error()
}

func (e nonRetryableError) Unwrap() error {
	return e.err
}

// NonRetryable marks an error as deterministic for writeback purposes. VFS
// should keep the pending staging state for inspection but must not keep
// calling the remote API for the same failing payload.
func NonRetryable(err error) error {
	if err == nil || IsNonRetryable(err) {
		return err
	}
	return nonRetryableError{err: err}
}

func IsNonRetryable(err error) bool {
	var target nonRetryableError
	return errors.As(err, &target)
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
