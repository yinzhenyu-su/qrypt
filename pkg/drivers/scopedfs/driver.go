// Package scopedfs exposes a platform-authorized directory as a qrypt drive.
//
// The driver is intentionally platform-neutral: Android SAF tree URIs,
// iOS security-scoped bookmarks, and future desktop folder grants are all
// adapted behind Backend. Go core only sees the normal drive.Driver contract.
package scopedfs

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	driverName     = "scopedfs"
	defaultBackend = "mobile"
	defaultRootID  = "root"
)

// Backend performs file operations inside a user-authorized directory.
//
// rootToken is an opaque, app-owned authorization token. On Android this can
// identify a persisted SAF tree URI; on iOS it can identify a stored
// security-scoped bookmark. id values are backend-owned stable document IDs.
type Backend interface {
	Stat(ctx context.Context, rootToken, id string) (drive.Entry, error)
	List(ctx context.Context, rootToken, parentID string) ([]drive.Entry, error)
	OpenRead(ctx context.Context, rootToken, id string, offset int64) (io.ReadCloser, error)
	Mkdir(ctx context.Context, rootToken, parentID, name string) (drive.Entry, error)
	Move(ctx context.Context, rootToken string, entry drive.Entry, dstParentID string) error
	Rename(ctx context.Context, rootToken string, entry drive.Entry, newName string) error
	Remove(ctx context.Context, rootToken string, entry drive.Entry) error
	CreateWrite(ctx context.Context, rootToken, parentID, name string) (WriteHandle, error)
}

// WriteHandle is a streaming write opened by Backend.CreateWrite.
type WriteHandle interface {
	io.Writer
	// Close commits the write and returns the final backend entry.
	Close() (drive.Entry, error)
	// Abort discards a failed or canceled write when the platform supports it.
	Abort() error
}

type Driver struct {
	drive.UnsupportedOperations
	backendName string
	rootToken   string
	rootID      string
}

type Options struct {
	Backend   string
	RootToken string
	RootID    string
}

var (
	backendMu sync.RWMutex
	backends  = map[string]Backend{}
)

func init() {
	drive.Register(driverName, func(params drive.Params) (drive.Driver, error) {
		return New(Options{
			Backend:   params["backend"],
			RootToken: params["root_token"],
			RootID:    params["root_id"],
		})
	},
		drive.ParamDef{
			Name:        "root_token",
			Type:        "string",
			Required:    true,
			Secret:      true,
			Description: "Opaque platform authorization token for the selected directory",
			Example:     "content://com.android.externalstorage.documents/tree/primary%3AQrypt",
		},
		drive.ParamDef{
			Name:        "root_id",
			Type:        "string",
			Description: "Backend document id for the authorized root directory",
			Default:     defaultRootID,
		},
		drive.ParamDef{
			Name:        "backend",
			Type:        "string",
			Description: "Registered scopedfs backend name",
			Default:     defaultBackend,
		},
	)
}

func New(opts Options) (*Driver, error) {
	if opts.RootToken == "" {
		return nil, fmt.Errorf("scopedfs: missing root_token")
	}
	if opts.Backend == "" {
		opts.Backend = defaultBackend
	}
	if opts.RootID == "" {
		opts.RootID = defaultRootID
	}
	return &Driver{backendName: opts.Backend, rootToken: opts.RootToken, rootID: opts.RootID}, nil
}

// RegisterBackend installs or replaces a platform backend. It is safe to call
// before or after driver construction; driver operations resolve the backend
// by name on each call so mobile reloads can swap implementations.
func RegisterBackend(name string, backend Backend) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("scopedfs: backend name required")
	}
	if backend == nil {
		return fmt.Errorf("scopedfs: backend %q is nil", name)
	}
	backendMu.Lock()
	defer backendMu.Unlock()
	backends[name] = backend
	return nil
}

func UnregisterBackend(name string) {
	backendMu.Lock()
	defer backendMu.Unlock()
	delete(backends, strings.TrimSpace(name))
}

func (d *Driver) Init(ctx context.Context) error {
	_, err := d.Stat(ctx, drive.Entry{ID: d.rootID, IsDir: true})
	if err != nil {
		return fmt.Errorf("scopedfs: stat root: %w", err)
	}
	return nil
}

func (d *Driver) Drop(ctx context.Context) error { return nil }

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parentID = d.resolveID(parentID)
	entries, err := d.backend().List(ctx, d.rootToken, parentID)
	if err != nil {
		return nil, fmt.Errorf("scopedfs: list: %w", err)
	}
	return entries, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rc, err := d.backend().OpenRead(ctx, d.rootToken, entry.ID, offset)
	if err != nil {
		return nil, fmt.Errorf("scopedfs: read: %w", err)
	}
	if size > 0 {
		return limitedReadCloser{Reader: io.LimitReader(rc, size), closer: rc}, nil
	}
	return rc, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	if err := ctx.Err(); err != nil {
		return drive.Entry{}, err
	}
	entry, err := d.backend().Mkdir(ctx, d.rootToken, d.resolveID(parentID), name)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("scopedfs: mkdir: %w", err)
	}
	return entry, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.backend().Move(ctx, d.rootToken, entry, d.resolveID(dstParentID)); err != nil {
		return fmt.Errorf("scopedfs: move: %w", err)
	}
	return nil
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.backend().Rename(ctx, d.rootToken, entry, newName); err != nil {
		return fmt.Errorf("scopedfs: rename: %w", err)
	}
	return nil
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.backend().Remove(ctx, d.rootToken, entry); err != nil {
		return fmt.Errorf("scopedfs: remove: %w", err)
	}
	return nil
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	if err := ctx.Err(); err != nil {
		return drive.Entry{}, err
	}
	if req.Source == nil {
		return drive.Entry{}, fmt.Errorf("scopedfs: upload source required")
	}
	body, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("scopedfs: open source: %w", err)
	}
	defer body.Close()
	dst, err := d.backend().CreateWrite(ctx, d.rootToken, d.resolveID(req.ParentID), req.Name)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("scopedfs: create write: %w", err)
	}
	if _, err := io.Copy(dst, drive.NewUploadProgressReader(req.Progress, body)); err != nil {
		_ = dst.Abort()
		return drive.Entry{}, fmt.Errorf("scopedfs: write: %w", err)
	}
	entry, err := dst.Close()
	if err != nil {
		_ = dst.Abort()
		return drive.Entry{}, fmt.Errorf("scopedfs: close write: %w", err)
	}
	return entry, nil
}

func (d *Driver) ResolvePath(ctx context.Context, path string) (string, error) {
	path = cleanPath(path)
	if path == "/" {
		return d.rootID, nil
	}
	parentID := d.rootID
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		children, err := d.List(ctx, parentID)
		if err != nil {
			return "", err
		}
		found := false
		for _, child := range children {
			if child.Name == part {
				parentID = child.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%w: scopedfs path %q", drive.ErrNotFound, path)
		}
	}
	return parentID, nil
}

func (d *Driver) ResolveRemoteName(ctx context.Context, plainName string) (drive.RemoteNameInfo, error) {
	return drive.RemoteNameInfo{PlainName: plainName, RemoteName: plainName}, nil
}

func (d *Driver) RequiredUploadHashes() []drive.HashAlgorithm {
	return nil
}

func (d *Driver) Capabilities() []drive.Capability {
	return []drive.Capability{
		drive.CapabilityPathResolver,
		drive.CapabilityRemoteNameResolver,
		drive.CapabilitySourceUploader,
		drive.CapabilityWriter,
	}
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{
		Driver:      driverName,
		Health:      drive.HealthLevelOK,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			drive.DebugStatRootID: d.rootID,
		},
		Extra: map[string]any{
			"backend": d.backendName,
		},
	}, nil
}

func (d *Driver) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

func (d *Driver) Stat(ctx context.Context, entry drive.Entry) (drive.Entry, error) {
	if err := ctx.Err(); err != nil {
		return drive.Entry{}, err
	}
	id := d.resolveID(entry.ID)
	got, err := d.backend().Stat(ctx, d.rootToken, id)
	if err != nil {
		return drive.Entry{}, err
	}
	return got, nil
}

func (d *Driver) backend() Backend {
	backendMu.RLock()
	backend := backends[d.backendName]
	backendMu.RUnlock()
	if backend == nil {
		return missingBackend{name: d.backendName}
	}
	return backend
}

func (d *Driver) resolveID(id string) string {
	if id == "" || id == "0" || id == "/" {
		return d.rootID
	}
	return id
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return "/"
	}
	path = strings.ReplaceAll(path, "\\", "/")
	parts := make([]string, 0)
	for _, part := range strings.Split(path, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "/"
	}
	return "/" + strings.Join(parts, "/")
}

type limitedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r limitedReadCloser) Close() error {
	return r.closer.Close()
}

type missingBackend struct {
	name string
}

func (b missingBackend) err() error {
	return fmt.Errorf("scopedfs: backend %q is not registered", b.name)
}

func (b missingBackend) Stat(context.Context, string, string) (drive.Entry, error) {
	return drive.Entry{}, b.err()
}

func (b missingBackend) List(context.Context, string, string) ([]drive.Entry, error) {
	return nil, b.err()
}

func (b missingBackend) OpenRead(context.Context, string, string, int64) (io.ReadCloser, error) {
	return nil, b.err()
}

func (b missingBackend) Mkdir(context.Context, string, string, string) (drive.Entry, error) {
	return drive.Entry{}, b.err()
}

func (b missingBackend) Move(context.Context, string, drive.Entry, string) error {
	return b.err()
}

func (b missingBackend) Rename(context.Context, string, drive.Entry, string) error {
	return b.err()
}

func (b missingBackend) Remove(context.Context, string, drive.Entry) error {
	return b.err()
}

func (b missingBackend) CreateWrite(context.Context, string, string, string) (WriteHandle, error) {
	return nil, b.err()
}

var _ drive.Driver = (*Driver)(nil)
