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
	ID        string    `json:"id"`
	ParentID  string    `json:"parent_id,omitempty"`
	Name      string    `json:"name"`
	IsDir     bool      `json:"is_dir"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Extra     any       `json:"-"`
}

type EntryRemoteNamer interface {
	EntryRemoteName() string
}

// RemoteHasher is an optional backend capability: the drive reports a content
// hash for a stored file without downloading it. The returned algorithm is
// one of HashMD5/HashSHA1/HashSHA256; the hash is hex-encoded.
//
// A driver that implements only RemoteHasher has its hashes treated as
// client-declared: upper layers may display them, but must not use them to
// conclude that content is unchanged. Drivers whose provider computes the hash
// (or that confirm it against stored bytes) should also implement
// ProvenanceHasher and report Verified=true.
type RemoteHasher interface {
	RemoteHash(ctx context.Context, entry Entry) (HashAlgorithm, string, error)
}

// RemoteHashSource labels where a remote hash came from.
type RemoteHashSource string

const (
	// RemoteHashSourceProvider means the provider computed or confirmed the
	// hash over the stored bytes.
	RemoteHashSourceProvider RemoteHashSource = "provider"
	// RemoteHashSourceVerified means qrypt confirmed the client-declared hash
	// against the provider's stored bytes, for example with an integrity check.
	RemoteHashSourceVerified RemoteHashSource = "verified"
	// RemoteHashSourceDeclared means the hash was supplied by a client and has
	// not been confirmed by the provider.
	RemoteHashSourceDeclared RemoteHashSource = "declared"
)

// RemoteHashInfo is a remote hash plus its provenance. Verified is the single
// authority bit: only a verified hash may justify skipping a transfer.
type RemoteHashInfo struct {
	Algorithm HashAlgorithm    `json:"algorithm"`
	Hash      string           `json:"hash"`
	Verified  bool             `json:"verified"`
	Source    RemoteHashSource `json:"source,omitempty"`
}

// ProvenanceHasher is the optional extension of RemoteHasher that labels a
// hash as provider-verified or client-declared. A driver that implements both
// is queried through ProvenanceHasher; a driver that implements only
// RemoteHasher is treated as returning declared hashes.
type ProvenanceHasher interface {
	RemoteHashInfo(ctx context.Context, entry Entry) (RemoteHashInfo, error)
}

// RemoteHashInfoFor asks a driver for a hash and normalizes its provenance.
// A legacy RemoteHasher result is marked declared, never verified, so callers
// cannot mistake it for an authoritative content check.
func RemoteHashInfoFor(ctx context.Context, d any, entry Entry) (RemoteHashInfo, error) {
	if hasher, ok := d.(ProvenanceHasher); ok {
		info, err := hasher.RemoteHashInfo(ctx, entry)
		if err != nil {
			return RemoteHashInfo{}, err
		}
		if info.Algorithm == "" || info.Hash == "" {
			return RemoteHashInfo{}, ErrUnsupported
		}
		if info.Source == "" {
			if info.Verified {
				info.Source = RemoteHashSourceProvider
			} else {
				info.Source = RemoteHashSourceDeclared
			}
		}
		return info, nil
	}
	if hasher, ok := d.(RemoteHasher); ok {
		algorithm, hash, err := hasher.RemoteHash(ctx, entry)
		if err != nil {
			return RemoteHashInfo{}, err
		}
		return RemoteHashInfo{
			Algorithm: algorithm,
			Hash:      hash,
			Verified:  false,
			Source:    RemoteHashSourceDeclared,
		}, nil
	}
	return RemoteHashInfo{}, ErrUnsupported
}

// ServerSideCopier is an optional backend capability: the driver can
// duplicate a stored object without a data round trip through qrypt
// (provider-side copy). The copy must be content-identical to the source.
// Providers that also declare CapabilityMtime must make Copy preserve the
// source mtime, otherwise sync's mtime comparison would re-copy on every
// run.
//
// This is a file-level operation: copying directories is not part of the
// contract and should return ErrUnsupported.
//
// Implementations do not need to honor overwrite semantics themselves;
// callers (drivecopy) remove an existing destination before invoking Copy.
type ServerSideCopier interface {
	Copy(ctx context.Context, src Entry, dstParentID, dstName string) (Entry, error)
}

// OfflineDownloader is an optional provider capability for cloud-side
// imports. The provider owns the remote task and its task identifier; qrypt
// only adapts its lifecycle into the application task model.
//
// The request carries a provider-neutral source plus the mount-relative
// destination the caller resolved; the result carries the provider's
// normalized observation. Providers map their own status model into
// OfflineState and keep provider-specific fields in the task detail layer.
type OfflineDownloader interface {
	CreateOfflineDownload(ctx context.Context, req OfflineDownloadRequest) (OfflineDownload, error)
	GetOfflineDownload(ctx context.Context, id string) (OfflineDownload, error)
	CancelOfflineDownload(ctx context.Context, id string) error
}

// OfflineSourceKindReporter is an optional interface a provider implements to
// declare the normalized source kinds it accepts. A provider that does not
// implement it is not pre-filtered; callers defer to the provider's own error
// at creation time.
type OfflineSourceKindReporter interface {
	OfflineSourceKinds() []OfflineSourceKind
}

// OfflineSourceKinds reports the source kinds d accepts, or nil when d does not
// report them. Callers that receive nil must not pre-reject a source; the
// provider decides at creation time.
func OfflineSourceKinds(d Driver) []OfflineSourceKind {
	if d == nil {
		return nil
	}
	reporter, ok := d.(OfflineSourceKindReporter)
	if !ok {
		return nil
	}
	return reporter.OfflineSourceKinds()
}

// OfflineSourceKind is the normalized form of a source qrypt passes to a
// provider. Providers report the kinds they accept; a kind no provider accepts
// is rejected before any provider task is created.
type OfflineSourceKind string

const (
	OfflineSourceMagnet  OfflineSourceKind = "magnet"
	OfflineSourceHTTP    OfflineSourceKind = "http"
	OfflineSourceFTP     OfflineSourceKind = "ftp"
	OfflineSourceTorrent OfflineSourceKind = "torrent"
)

// OfflineSelectionMode names how a caller chooses which parsed files a
// provider should import.
type OfflineSelectionMode string

const (
	// OfflineSelectionAll imports every parsed file. It is the zero value.
	OfflineSelectionAll OfflineSelectionMode = "all"
	// OfflineSelectionLargest imports the Largest largest files.
	OfflineSelectionLargest OfflineSelectionMode = "largest"
	// OfflineSelectionInclude imports files matching Include and not Exclude.
	OfflineSelectionInclude OfflineSelectionMode = "include"
)

// OfflineSelection is the provider-neutral file selection applied to a parsed
// resource before submission. The zero value means "all files".
type OfflineSelection struct {
	Mode    OfflineSelectionMode `json:"mode,omitempty"`
	Largest int                  `json:"largest,omitempty"`
	Include []string             `json:"include,omitempty"`
	Exclude []string             `json:"exclude,omitempty"`
}

// OfflineDownloadRequest is the provider-neutral input for staging a source in
// a provider's cloud storage. Destination is a mount-relative path the
// provider resolves to its own directory identity.
type OfflineDownloadRequest struct {
	// SourceKind is the normalized source kind; empty means the provider
	// should infer it from Source.
	SourceKind OfflineSourceKind `json:"source_kind,omitempty"`
	// Source is a magnet link or URL.
	Source string `json:"source"`
	// Title overrides the provider-side resource title when non-empty.
	Title string `json:"title,omitempty"`
	// Destination is the mount-relative staging directory path.
	Destination string `json:"destination"`
	// DestinationID is the provider's own identity for Destination when the
	// caller already resolved it (for example the fid VFS Stat/Mkdir returned).
	// Providers that can use it directly avoid re-resolving the path, which is
	// important when their directory listing is eventually consistent.
	DestinationID string `json:"destination_id,omitempty"`
	// Selection chooses which parsed files to import; zero value means all.
	Selection OfflineSelection `json:"selection,omitempty"`
	// Accelerated asks the provider to obtain an accelerated download session
	// for the staged resource. The caller sets it only when the provider
	// declared CapabilityAcceleratedFetch; providers may ignore it.
	Accelerated bool `json:"accelerated,omitempty"`
}

// OfflineState is the canonical provider observation state. Providers map
// their own status model into exactly these values.
type OfflineState string

const (
	OfflineStateQueued    OfflineState = "queued"
	OfflineStateRunning   OfflineState = "running"
	OfflineStateSucceeded OfflineState = "succeeded"
	OfflineStateFailed    OfflineState = "failed"
	OfflineStateCanceled  OfflineState = "canceled"
)

// OfflineProgress is the provider-reported progress of the staging task.
// Percent is 0..100 and is zero when the provider reports no progress model.
type OfflineProgress struct {
	Percent    float64 `json:"percent,omitempty"`
	BytesDone  int64   `json:"bytes_done,omitempty"`
	BytesTotal int64   `json:"bytes_total,omitempty"`
	FilesDone  int64   `json:"files_done,omitempty"`
	FilesTotal int64   `json:"files_total,omitempty"`
}

// OfflineFile is one result file reported by the provider. RemoteID is the
// provider's identity for the stored entry when it exposes one. Path is the
// file's location relative to the staging destination (POSIX style, no leading
// slash) when the provider can report it; a provider that cannot leaves it
// empty and the caller falls back to Name.
type OfflineFile struct {
	Index    int    `json:"index,omitempty"`
	Name     string `json:"name,omitempty"`
	Size     int64  `json:"size,omitempty"`
	RemoteID string `json:"remote_id,omitempty"`
	Path     string `json:"path,omitempty"`
}

// OfflineDownload is a provider's normalized observation of one cloud-side
// offline task. ID and State are always set; the remaining fields are filled
// in as the provider exposes them. Available reports that the provider's staged
// result set is complete and no longer changing (for example an instant-import
// resource), so a fetch may resolve the layout and transfer before the task
// reaches a terminal state.
type OfflineDownload struct {
	ID          string          `json:"id"`
	State       OfflineState    `json:"state"`
	Progress    OfflineProgress `json:"progress,omitempty"`
	Destination string          `json:"destination,omitempty"`
	RemoteID    string          `json:"remote_id,omitempty"`
	Files       []OfflineFile   `json:"files,omitempty"`
	Available   bool            `json:"available,omitempty"`
	ErrorCode   string          `json:"error_code,omitempty"`
	Error       string          `json:"error,omitempty"`
}

type EntryRawExtraer interface {
	EntryRawExtra() any
}

// EntryResolver is an optional provider capability: fetch one entry by its
// provider id. Core uses it when an entry's private metadata is missing, and
// falls back to listing when a driver does not implement it.
type EntryResolver interface {
	Stat(ctx context.Context, id string) (Entry, error)
}

// RemoteNameCodec is an optional provider capability for names the backend
// cannot store unchanged. The plain name is what callers see; the remote form
// is what the provider stores and what reads, writes, renames, and copies use.
// A provider whose storage preserves every valid qrypt name must not implement
// it.
type RemoteNameCodec interface {
	Encode(plain string) (remote string, err error)
	Decode(remote string) (plain string, ok bool)
}

// WrapRemoteName attaches a provider-side name to an entry while preserving
// its existing private metadata. Drivers that escape names use this so a
// listing never leaks the escaped form to callers.
func WrapRemoteName(entry Entry, remote string) Entry {
	if remote == "" || remote == entry.Name {
		return entry
	}
	entry.Extra = EntryExtraWrapper{RemoteName: remote, Raw: RawEntryExtra(entry.Extra)}
	return entry
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

// RemoteHashFromExtra reads a hex-encoded content hash stored under key in
// the entry's Extra map. Backends that carry provider hashes in list
// metadata (e.g. baidunetdisk md5, onedrive sha1Hash) share this pattern;
// ErrUnsupported is returned when the entry lacks the hash so callers can
// degrade cleanly.
func RemoteHashFromExtra(entry Entry, key string, algorithm HashAlgorithm) (HashAlgorithm, string, error) {
	raw := RawEntryExtra(entry.Extra)
	m, ok := raw.(map[string]any)
	if !ok {
		return "", "", ErrUnsupported
	}
	hash, ok := m[key].(string)
	if !ok || hash == "" {
		return "", "", ErrUnsupported
	}
	return algorithm, hash, nil
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
	// Move relocates entry under dstParentID. A backend that derives an
	// entry's id from its location must return the id of the new location; a
	// backend with provider-assigned ids must keep that id stable across the
	// move and return it unchanged with the new parent id.
	Move(ctx context.Context, entry Entry, dstParentID string) (Entry, error)
	// Rename renames entry in place; the returned entry follows the same id
	// rule as Move.
	Rename(ctx context.Context, entry Entry, newName string) (Entry, error)
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

// EncryptedHashProvider is the optional driver extension backing
// vfs.EncryptedHash: it computes the backend hash the plaintext would have
// when encrypted with the mount's cipher. Not every backend stores cipher
// metadata in a way that supports this; absent drivers keep returning
// ErrUnsupported.
type EncryptedHashProvider interface {
	EncryptedHash(ctx context.Context, entry Entry, plain io.Reader, plainSize int64, algorithm HashAlgorithm) (string, error)
}

// Space describes backend capacity in bytes.
type Space struct {
	Total int64
	Free  int64
}

var ErrUnsupported = errors.New("drive: operation unsupported")
var ErrSpaceUnsupported = errors.New("drive: space query unsupported")

// ErrNotFound is the stable sentinel for "the referenced object does not
// exist". Drivers wrap it when a backend reports a missing resource; vfs
// aliases it as vfs.ErrNotFound so both layers classify with errors.Is.
var ErrNotFound = errors.New("drive: not found")

// ErrAuth is the stable sentinel for authentication/authorization failure
// (missing, expired or rejected credentials). Drivers wrap it on 401-style
// responses and expired-token replies so retry layers can refresh
// credentials instead of re-running the call blindly.
var ErrAuth = errors.New("drive: authentication required")

// ErrRateLimit is the stable sentinel for throttling (HTTP 429 or backend
// quota limits). Drivers wrap it so backoff layers can apply longer waits.
var ErrRateLimit = errors.New("drive: rate limit exceeded")

// ErrInvalidInput is the stable sentinel for request or payload validation
// failures (HTTP 400/422, malformed upload results). Retrying never helps;
// callers can surface it to the user as a data problem.
var ErrInvalidInput = errors.New("drive: invalid input")

// ErrPermission is the stable sentinel for an authenticated principal that is
// not allowed to perform the operation (HTTP 403). Retrying never helps.
var ErrPermission = errors.New("drive: permission denied")

// ErrConflict is the stable sentinel for a state conflict such as an existing
// destination name or a concurrent mutation (HTTP 409). Retrying usually does
// not help without changing the request.
var ErrConflict = errors.New("drive: conflict")

// UnsupportedOperations provides default implementations for Driver methods
// that a read-only or partial driver intentionally does not advertise in
// Capabilities.
type UnsupportedOperations struct{}

func (UnsupportedOperations) Mkdir(context.Context, string, string) (Entry, error) {
	return Entry{}, ErrUnsupported
}

func (UnsupportedOperations) Move(context.Context, Entry, string) (Entry, error) {
	return Entry{}, ErrUnsupported
}

func (UnsupportedOperations) Rename(context.Context, Entry, string) (Entry, error) {
	return Entry{}, ErrUnsupported
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

// NonRetryable marks an error as deterministic for upload purposes. VFS
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
	Name            string            `json:"name"`
	Type            string            `json:"type,omitempty"` // "string" (default), "int", "bool", "duration"
	Required        bool              `json:"required,omitempty"`
	Secret          bool              `json:"secret,omitempty"` // masked in debug output and help
	Scope           string            `json:"scope,omitempty"`  // credential | mount | both
	Description     string            `json:"description,omitempty"`
	DescriptionI18n map[string]string `json:"-"`
	Default         string            `json:"default,omitempty"`
	Example         string            `json:"example,omitempty"`
}

// Parameter scopes decide where a parameter may be configured. A credential
// supplies account-scoped parameters, a mount supplies path- and
// instance-scoped parameters, and "both" may appear in either place with a
// mount value taking precedence.
const (
	ParamScopeCredential = "credential"
	ParamScopeMount      = "mount"
	ParamScopeBoth       = "both"
)

// AllowsCredential reports whether the parameter may be supplied by a
// credential. Dynamically registered drivers that declare no scope keep the
// pre-scope behaviour and may supply it anywhere.
func (p ParamDef) AllowsCredential() bool {
	return p.Scope == "" || p.Scope == ParamScopeCredential || p.Scope == ParamScopeBoth
}

// AllowsMount reports whether the parameter may be supplied by a mount.
func (p ParamDef) AllowsMount() bool {
	return p.Scope == "" || p.Scope == ParamScopeMount || p.Scope == ParamScopeBoth
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
	return ParamSpecFor(name).Params
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
