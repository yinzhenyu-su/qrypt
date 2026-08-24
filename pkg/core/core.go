package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type Options struct {
	ConfigPath     string
	Runtime        RuntimeLayout
	MountName      string
	ForceNamespace bool
	ReadChunkLimit int
	UploadSources  UploadSourceProvider
	// Bandwidth overrides the config [bandwidth] section when set (CLI flags).
	Bandwidth *config.BandwidthLimits
}

type UploadSourceProvider interface {
	OpenUploadSource(ctx context.Context, token string, offset int64) (io.ReadCloser, error)
}

type Core struct {
	fs                 BuiltFileSystem
	cleanup            func()
	configPath         string
	runtimeLayout      RuntimeLayout
	readCacheDir       string
	thumbnailDir       string
	thumbnailMax       int64
	uploadDir          string
	defaultUploadMount string
	defaultUploadPath  string
	debugServer        *control.Server
	tasks              *task.Manager
	uploadSources      UploadSourceProvider
	readChunkLimit     int
	vfsCancel          context.CancelFunc
	streamsMu          sync.Mutex
	downloadStreams    map[string]*downloadStreamBatch
	uploadStreams      map[string]*uploadStreamBatch
}

type RuntimeLayout struct {
	RootDir      string
	ConfigDir    string
	ReadCacheDir string
	ThumbnailDir string
	UploadDir    string
	StateDir     string
	DriverDir    string
	LogDir       string
	TmpDir       string
}

func Open(ctx context.Context, opts Options) (*Core, error) {
	if opts.ConfigPath == "" {
		return nil, fmt.Errorf("core: config path required")
	}
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	runtime := NewStorageLayout(cfg, opts.Runtime)
	if err := ensureRuntimeLayout(runtime); err != nil {
		return nil, err
	}
	if err := initRuntimeLogger(cfg, runtime); err != nil {
		return nil, err
	}
	fs, cleanup, err := BuildFileSystem(ctx, cfg, opts)
	if err != nil {
		return nil, err
	}
	// The VFS upload workers derive from this context; keep our own cancel
	// so Close stops them even when the caller passed a background context
	// (mobile session layer). Callers who cancel their own context also
	// stop the workers, and cancel is idempotent.
	vfsCtx, vfsCancel := context.WithCancel(ctx)
	fs.Start(vfsCtx)
	// An unset thumbnail_cache.max_size falls back to the default; an
	// explicit "0" disables thumbnail caching (thumbnail.go short-circuits
	// on thumbnailMax <= 0).
	thumbnailMax := cfg.ThumbnailCache.MaxSizeBytes()
	if cfg.ThumbnailCache.MaxSize == "" {
		thumbnailMax = 256 << 20
	}
	readChunkLimit := opts.ReadChunkLimit
	if readChunkLimit <= 0 {
		readChunkLimit = DefaultReadChunkLimit
	}
	c := &Core{fs: fs, cleanup: cleanup, configPath: opts.ConfigPath, runtimeLayout: runtime, readCacheDir: runtime.ReadCacheDir, thumbnailDir: runtime.ThumbnailDir, thumbnailMax: thumbnailMax, uploadDir: runtime.UploadDir, defaultUploadMount: cfg.Upload.DefaultMount, defaultUploadPath: cfg.Upload.DefaultPath, uploadSources: opts.UploadSources, readChunkLimit: readChunkLimit, vfsCancel: vfsCancel}
	c.tasks = c.newTaskManager()
	if cfg.Debug.Enabled {
		if err := c.StartDebugServer(ctx, cfg.Debug.EffectiveListen()); err != nil {
			// A previous process may still hold the debug socket (e.g. a
			// sticky service restarted after the app was relaunched). Debug
			// is a diagnostics-only surface: log and continue with a working
			// core instead of failing the whole open.
			logging.L.Warnf("core: debug server on %s failed: %v (continuing without debug server)", cfg.Debug.EffectiveListen(), err)
		}
	}
	return c, nil
}

func (c *Core) FileSystem() vfs.FileSystem {
	if c == nil {
		return nil
	}
	return c.fs
}

func (c *Core) Stat(ctx context.Context, path string) (drive.Entry, error) {
	if c == nil || c.fs == nil {
		return drive.Entry{}, fmt.Errorf("core: closed")
	}
	return c.fs.Stat(ctx, path)
}

func (c *Core) List(ctx context.Context, path string) ([]drive.Entry, error) {
	if c == nil || c.fs == nil {
		return nil, fmt.Errorf("core: closed")
	}
	return c.fs.List(ctx, path)
}

type listPager interface {
	ListPage(ctx context.Context, path, cursor string, limit int) (vfs.ListPageResult, error)
}

// ListPage returns a deterministic slice of a directory listing (sorted by
// name) with a cursor for incremental browsing. limit <= 0 returns the whole
// listing.
func (c *Core) ListPage(ctx context.Context, path, cursor string, limit int) (vfs.ListPageResult, error) {
	if c == nil || c.fs == nil {
		return vfs.ListPageResult{}, fmt.Errorf("core: closed")
	}
	pager, ok := c.fs.(listPager)
	if !ok {
		return vfs.ListPageResult{}, fmt.Errorf("core: list paging unsupported")
	}
	return pager.ListPage(ctx, path, cursor, limit)
}

func (c *Core) Mkdir(ctx context.Context, path string) (drive.Entry, error) {
	if c == nil || c.fs == nil {
		return drive.Entry{}, fmt.Errorf("core: closed")
	}
	return c.fs.Mkdir(ctx, path)
}

func (c *Core) Rename(ctx context.Context, oldPath, newPath string) error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	return c.fs.Rename(ctx, oldPath, newPath)
}

// RefreshPath clears the directory listing cache for path.
func (c *Core) RefreshPath(path string) {
	if c == nil || c.fs == nil {
		return
	}
	c.fs.RefreshPath(path)
}

func (c *Core) Remove(ctx context.Context, path string) error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	item, err := c.fs.Stat(ctx, path)
	if err != nil {
		return err
	}
	if item.IsDir {
		return c.fs.RemoveDir(ctx, path)
	}
	return c.fs.Remove(ctx, path)
}

func (c *Core) Capabilities(ctx context.Context, path string) (vfs.CapabilityInfo, error) {
	if c == nil || c.fs == nil {
		return vfs.CapabilityInfo{}, fmt.Errorf("core: closed")
	}
	reporter, ok := c.fs.(vfs.CapabilityReporter)
	if !ok {
		return vfs.CapabilityInfo{}, fmt.Errorf("core: capability query unavailable")
	}
	return reporter.CapabilitiesForPath(ctx, path)
}

func (c *Core) Mounts() ([]vfs.MountInfo, error) {
	if c == nil || c.fs == nil {
		return nil, fmt.Errorf("core: closed")
	}
	reporter, ok := c.fs.(vfs.MountReporter)
	if !ok {
		return nil, fmt.Errorf("core: mount query unavailable")
	}
	mounts := reporter.Mounts()
	out := make([]vfs.MountInfo, len(mounts))
	copy(out, mounts)
	return out, nil
}

func (c *Core) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	var errs []error
	// The task manager owns long-running goroutines (stream pollers, batch
	// runners) whose contexts derive from the manager, not from any
	// caller-passed context; close it even when no cleanup is registered
	// (directly constructed cores used by tests).
	if c.tasks != nil {
		c.tasks.Close()
	}
	// Stop the VFS workers even if the caller's context is still alive
	// (e.g. a background context from the mobile session layer).
	if c.vfsCancel != nil {
		c.vfsCancel()
	}
	// Wait for the filesystem to finish tearing down (workers, journal and
	// staging writes, read-cache flush) before releasing external resources.
	// Cancelling the context only triggers an ASYNCHRONOUS Close via the
	// Start hook; waiting here gives the same guarantee as an explicit
	// Close: after Core.Close returns, no filesystem-owned goroutine writes
	// to the storage directories.
	//
	// If the filesystem did not finish (ctx deadline or cancel), DO NOT
	// clean up external resources while its workers may still write to
	// them, and keep c.fs so the caller can retry Close later. Teardown
	// keeps running in the background and a retried Close waits for it.
	if fs := c.fs; fs != nil {
		if err := fs.Close(ctx); err != nil {
			return errors.Join(append(errs, err)...)
		}
	}
	if c.cleanup == nil {
		c.fs = nil
		return errors.Join(errs...)
	}
	if err := c.StopDebugServer(ctx); err != nil {
		errs = append(errs, err)
	}
	c.cleanup()
	c.cleanup = nil
	c.fs = nil
	return errors.Join(errs...)
}

func DriverNames() []string {
	return drive.Names()
}

func DriverSchema(name string) []drive.ParamDef {
	return drive.ParamSchema(name)
}

func DriverNamesJSON() (string, error) {
	return marshalJSON(DriverNames())
}

func DriverSchemaJSON(name string) (string, error) {
	return marshalJSON(DriverSchema(name))
}

// BuiltFileSystem is the filesystem surface BuildFileSystem returns: full
// file operations plus lifecycle (Start), cache-refresh (RefreshPath),
// and task-source introspection (TaskSource) so the service layer can
// aggregate upload and delete activity into the task manager.
type BuiltFileSystem interface {
	vfs.FileSystem
	vfs.Lifecycle
	vfs.PathRefresher
	TaskSource() task.Source
}

// ReadStream opens a bounded-memory sequential reader when the filesystem
// provides the optional streaming surface.
func (c *Core) ReadStream(ctx context.Context, path string) (io.ReadCloser, error) {
	if c == nil || c.fs == nil {
		return nil, fmt.Errorf("core: closed")
	}
	streamer, ok := c.fs.(vfs.StreamReader)
	if !ok {
		return nil, fmt.Errorf("core: sequential read stream unsupported")
	}
	return streamer.ReadStream(ctx, path)
}

// ReleaseReadSession forgets access-pattern state associated with a mobile
// or mounted open-file handle.
func (c *Core) ReleaseReadSession(sessionID uint64) {
	if c == nil || c.fs == nil {
		return
	}
	if releaser, ok := c.fs.(interface{ ReleaseReadSession(uint64) }); ok {
		releaser.ReleaseReadSession(sessionID)
	}
}

func BuildFileSystem(ctx context.Context, cfg *config.Config, opts Options) (BuiltFileSystem, func(), error) {
	if err := config.Validate(cfg); err != nil {
		return nil, nil, err
	}
	limits, err := cfg.EffectiveBandwidthLimits()
	if err != nil {
		return nil, nil, err
	}
	if opts.Bandwidth != nil {
		limits = *opts.Bandwidth
	}
	return buildNamespace(ctx, cfg, NewStorageLayout(cfg, opts.Runtime), bandwidthLimiter(limits), opts)
}

func bandwidthLimiter(limits config.BandwidthLimits) *drive.BandwidthLimiter {
	return drive.NewBandwidthLimiter(drive.BandwidthLimits{
		DownloadBytesPerSecond: limits.DownloadBytesPerSecond,
		UploadBytesPerSecond:   limits.UploadBytesPerSecond,
	})
}

func EffectiveCacheDir(cfg *config.Config, runtime RuntimeLayout) string {
	return NewStorageLayout(cfg, runtime).ReadCacheDir
}

func DefaultCacheDir() string {
	return filepath.Join(qryptHomeDir(), "cache")
}

func DefaultUploadDir() string {
	return filepath.Join(qryptHomeDir(), "upload")
}

func DefaultStateDir() string {
	return filepath.Join(qryptHomeDir(), "state")
}

// qryptHomeDir returns the qrypt data root. It defaults to ~/.qrypt but can
// be redirected with QRYPT_HOME so portable installs and test runs never
// touch the user's real state.
func qryptHomeDir() string {
	if home := strings.TrimSpace(os.Getenv("QRYPT_HOME")); home != "" {
		return filepath.Clean(util.ExpandHome(home))
	}
	return util.ExpandHome("~/.qrypt")
}

func NewStorageLayout(cfg *config.Config, runtime RuntimeLayout) RuntimeLayout {
	storage := config.StorageConfig{}
	if cfg != nil {
		storage = cfg.Storage
	}
	workDir := ""
	if home := strings.TrimSpace(os.Getenv("QRYPT_HOME")); home != "" {
		// QRYPT_HOME is the process-wide isolation/portable-install override;
		// no configured child directory may escape it.
		storage = config.StorageConfig{}
		workDir = filepath.Clean(util.ExpandHome(home))
	} else {
		workDir = util.ExpandHome(storage.WorkDir)
		if workDir == "" {
			workDir = qryptHomeDir()
		}
	}
	readCacheDir := util.ExpandHome(storage.ReadCacheDir)
	if readCacheDir == "" {
		readCacheDir = filepath.Join(workDir, "cache", "read")
	}
	thumbnailDir := util.ExpandHome(storage.ThumbnailCacheDir)
	if thumbnailDir == "" {
		thumbnailDir = filepath.Join(workDir, "cache", "thumbnail")
	}
	uploadDir := util.ExpandHome(storage.UploadDir)
	if uploadDir == "" {
		uploadDir = filepath.Join(workDir, "upload")
	}
	stateDir := util.ExpandHome(storage.StateDir)
	if stateDir == "" {
		stateDir = filepath.Join(workDir, "state")
	}
	logDir := util.ExpandHome(storage.LogDir)
	if logDir == "" {
		logDir = filepath.Join(workDir, "logs")
	}
	tmpDir := util.ExpandHome(storage.TmpDir)
	if tmpDir == "" {
		tmpDir = filepath.Join(workDir, "tmp")
	}
	layout := RuntimeLayout{
		RootDir:      workDir,
		ReadCacheDir: readCacheDir,
		ThumbnailDir: thumbnailDir,
		UploadDir:    uploadDir,
		StateDir:     stateDir,
		DriverDir:    filepath.Join(stateDir, "driver"),
		LogDir:       logDir,
		TmpDir:       tmpDir,
	}
	return mergeRuntimeLayout(layout, runtime)
}

func mergeRuntimeLayout(base, override RuntimeLayout) RuntimeLayout {
	if override.RootDir != "" {
		base.RootDir = util.ExpandHome(override.RootDir)
	}
	if override.ConfigDir != "" {
		base.ConfigDir = util.ExpandHome(override.ConfigDir)
	}
	if override.ReadCacheDir != "" {
		base.ReadCacheDir = util.ExpandHome(override.ReadCacheDir)
	}
	if override.ThumbnailDir != "" {
		base.ThumbnailDir = util.ExpandHome(override.ThumbnailDir)
	}
	if override.UploadDir != "" {
		base.UploadDir = util.ExpandHome(override.UploadDir)
	}
	if override.StateDir != "" {
		base.StateDir = util.ExpandHome(override.StateDir)
	}
	if override.DriverDir != "" {
		base.DriverDir = util.ExpandHome(override.DriverDir)
	} else if override.StateDir != "" {
		base.DriverDir = filepath.Join(base.StateDir, "driver")
	}
	if override.LogDir != "" {
		base.LogDir = util.ExpandHome(override.LogDir)
	}
	if override.TmpDir != "" {
		base.TmpDir = util.ExpandHome(override.TmpDir)
	}
	return base
}

func ensureRuntimeLayout(layout RuntimeLayout) error {
	for _, dir := range []string{layout.ConfigDir, layout.ReadCacheDir, layout.ThumbnailDir, layout.UploadDir, layout.StateDir, layout.DriverDir, layout.LogDir, layout.TmpDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

type mountBuildResult struct {
	mount  vfs.Mount
	driver drive.Driver
	err    error
}

func buildNamespace(ctx context.Context, cfg *config.Config, layout RuntimeLayout, limiter *drive.BandwidthLimiter, opts Options) (BuiltFileSystem, func(), error) {
	var mountConfigs []config.MountConfig
	for _, mountCfg := range cfg.Mounts {
		if opts.MountName != "" && mountCfg.Name != opts.MountName {
			continue
		}
		mountConfigs = append(mountConfigs, mountCfg)
	}
	if len(mountConfigs) == 0 {
		if opts.MountName != "" {
			return nil, nil, fmt.Errorf("config: mount %q not found", opts.MountName)
		}
		return nil, nil, fmt.Errorf("config: no mounts selected")
	}

	results := make([]mountBuildResult, len(mountConfigs))
	var wg sync.WaitGroup
	for i, mountCfg := range mountConfigs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = buildMount(ctx, cfg, layout, limiter, mountCfg)
		}()
	}
	wg.Wait()

	mounts := make([]vfs.Mount, 0, len(results))
	drivers := make([]drive.Driver, 0, len(results))
	var buildErr error
	for _, result := range results {
		if result.driver != nil {
			drivers = append(drivers, result.driver)
		}
		if result.err != nil && buildErr == nil {
			buildErr = result.err
		}
		if result.mount.FS != nil {
			mounts = append(mounts, result.mount)
		}
	}
	if buildErr != nil {
		closeMounts(mounts)
		dropAll(ctx, drivers)
		return nil, nil, buildErr
	}

	if opts.MountName != "" && !opts.ForceNamespace {
		fs := mounts[0].FS
		return fs, func() {
			flushReadCache(fs)
			closeMounts(mounts)
			dropAll(ctx, drivers)
		}, nil
	}
	ns, err := vfs.NewNamespace(mounts)
	if err != nil {
		closeMounts(mounts)
		dropAll(ctx, drivers)
		return nil, nil, err
	}
	return ns, func() {
		flushReadCache(ns)
		closeMounts(mounts)
		dropAll(ctx, drivers)
	}, nil
}

func buildMount(ctx context.Context, cfg *config.Config, layout RuntimeLayout, limiter *drive.BandwidthLimiter, mountCfg config.MountConfig) (result mountBuildResult) {
	params := drive.Params{}
	for key, value := range mountCfg.Params {
		params[key] = value
	}
	readCache := cfg.ReadCacheFor(mountCfg.Name)
	upload := cfg.UploadFor(mountCfg.Name)
	mountReadCacheDir := filepath.Join(layout.ReadCacheDir, mountCfg.Name)
	mountUploadDir := filepath.Join(layout.UploadDir, mountCfg.Name)
	stateDir := driverStateDir(layout, mountCfg.Name)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		result.err = err
		return result
	}
	raw, err := drive.New(mountCfg.Type, params)
	if err != nil {
		result.err = err
		return result
	}
	result.driver = raw
	installDriverStateStore(raw, stateDir)
	if err := raw.Init(ctx); err != nil {
		result.err = err
		return result
	}
	rootID, err := resolveMountRootID(ctx, raw)
	if err != nil {
		result.err = fmt.Errorf("config: mount %s resolve root: %w", mountCfg.Name, err)
		return result
	}
	var drv = drive.WrapBandwidthLimitedDriver(raw, limiter)
	enc := cfg.EncryptionFor(mountCfg.Name)
	if enc.Password != "" {
		if err := enc.Validate(); err != nil {
			result.err = err
			return result
		}
		cp, err := crypt.NewRcloneCipherFromConfig(enc)
		if err != nil {
			result.err = err
			return result
		}
		drv = crypt.NewDriver(drv, cp, crypt.DriverOptions{ContentDedup: enc.ContentDedup})
	}
	maxBytes := readCache.MaxSizeBytes()
	// An unset max_size falls back to the default; an explicit "0"
	// disables the read cache for this mount (the store short-circuits).
	if readCache.MaxSize == "" {
		maxBytes = 2 << 30
	}
	uploadDelay, err := config.ParseDuration(upload.UploadDelay)
	if err != nil {
		result.err = fmt.Errorf("config: mount %s invalid upload.upload_delay: %w", mountCfg.Name, err)
		return result
	}
	deleteDelay, err := config.ParseDuration(upload.DeleteDelay)
	if err != nil {
		result.err = fmt.Errorf("config: mount %s invalid upload.delete_delay: %w", mountCfg.Name, err)
		return result
	}
	if upload.UploadWorkers < 0 {
		result.err = fmt.Errorf("config: mount %s invalid upload.upload_workers: must be non-negative", mountCfg.Name)
		return result
	}
	fs, err := vfs.New(drv, vfs.Options{
		Name:          mountCfg.Name,
		ReadCacheDir:  mountReadCacheDir,
		UploadDir:     mountUploadDir,
		CacheMaxBytes: maxBytes,
		RootID:        rootID,
		Encrypted:     enc.Password != "",
		TestEnabled:   mountCfg.TestEnabled,
		UploadDelay:   uploadDelay,
		UploadWorkers: upload.UploadWorkers,
		DeleteDelay:   deleteDelay,
	})
	if err != nil {
		result.err = err
		return result
	}
	result.mount = vfs.Mount{Name: mountCfg.Name, FS: fs}
	return result
}

func resolveMountRootID(ctx context.Context, driver drive.Driver) (string, error) {
	if !drive.HasCapability(driver, drive.CapabilityPathResolver) {
		return "", nil
	}
	return driver.ResolvePath(ctx, "/")
}

func driverStateDir(layout RuntimeLayout, mountName string) string {
	return filepath.Join(layout.DriverDir, mountName)
}

func installDriverStateStore(driver drive.Driver, stateDir string) {
	if installer, ok := driver.(drive.StateStoreInstaller); ok {
		_ = os.MkdirAll(stateDir, 0o700)
		installer.InstallStateStore(drive.NewFileStateStore(stateDir))
	}
}

func dropAll(ctx context.Context, drivers []drive.Driver) {
	for _, drv := range drivers {
		_ = drv.Drop(ctx)
	}
}

// closeMounts stops the background goroutines of VFS instances created so
// far. buildNamespace error paths call it before dropping drivers: vfs.New
// already started the read-cache writer, so leaking it would leave a
// goroutine behind on every failed namespace build.
func closeMounts(mounts []vfs.Mount) {
	for _, m := range mounts {
		_ = m.FS.CloseReadCache()
	}
}

func initRuntimeLogger(cfg *config.Config, layout RuntimeLayout) error {
	if layout.LogDir == "" {
		return nil
	}
	level := "info"
	if cfg != nil && strings.TrimSpace(cfg.Logging.LogLevel) != "" {
		level = cfg.Logging.LogLevel
	}
	// Explicit log_file/error_file win; otherwise fall back to
	// <storage.log_dir>/qrypt.log and qrypt-error.log. layout.LogDir is the
	// expanded explicit log directory or the directory derived from work_dir,
	// so the defaults match the config layer's effective log paths.
	logFile := ""
	errFile := ""
	if cfg != nil {
		logFile = util.ExpandHome(cfg.EffectiveLogFile())
		errFile = util.ExpandHome(cfg.EffectiveErrorFile())
	}
	if logFile == "" {
		logFile = filepath.Join(layout.LogDir, "qrypt.log")
	}
	if errFile == "" {
		errFile = filepath.Join(layout.LogDir, "qrypt-error.log")
	}
	newLogger, err := logging.New(level, logFile, errFile, nil)
	if err != nil {
		return fmt.Errorf("initialize runtime logging: %w", err)
	}
	logging.ReplaceDefault(newLogger)
	logging.L.Infof("[CORE] runtime logging initialized")
	return nil
}

func marshalJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func JoinPath(parent, name string) string {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		parent = "/"
	}
	if parent == "/" {
		return "/" + strings.Trim(name, "/")
	}
	return strings.TrimRight(parent, "/") + "/" + strings.Trim(name, "/")
}

func TimeoutContext(timeoutMS int) (context.Context, context.CancelFunc) {
	if timeoutMS <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
}

type readCacheFlusher interface {
	FlushReadCache() error
}

type readCacheCloser interface {
	CloseReadCache() error
}

func flushReadCache(fs any) {
	if closer, ok := fs.(readCacheCloser); ok {
		if err := closer.CloseReadCache(); err != nil {
			logging.L.Warnf("[CACHE] close read cache failed: %v", err)
		}
		return
	}
	if flusher, ok := fs.(readCacheFlusher); ok {
		if err := flusher.FlushReadCache(); err != nil {
			logging.L.Warnf("[CACHE] flush read cache failed: %v", err)
		}
	}
}
