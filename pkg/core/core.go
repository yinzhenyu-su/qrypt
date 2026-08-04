package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type Options struct {
	ConfigPath     string
	Runtime        RuntimeLayout
	MountName      string
	ForceNamespace bool
	ReadChunkLimit int
}

type Core struct {
	fs                 vfs.FileSystem
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
	fs.Start(ctx)
	c := &Core{fs: fs, cleanup: cleanup, configPath: opts.ConfigPath, runtimeLayout: runtime, readCacheDir: runtime.ReadCacheDir, thumbnailDir: runtime.ThumbnailDir, thumbnailMax: cfg.ThumbnailCache.MaxSizeBytes(), uploadDir: runtime.UploadDir, defaultUploadMount: cfg.Upload.DefaultMount, defaultUploadPath: cfg.Upload.DefaultPath}
	c.tasks = c.newTaskManager()
	if cfg.Debug.Enabled {
		if err := c.StartDebugServer(ctx, cfg.Debug.EffectiveListen()); err != nil {
			c.Close(context.Background())
			return nil, err
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
	if c == nil || c.cleanup == nil {
		return nil
	}
	if c.tasks != nil {
		c.tasks.Close()
	}
	_ = c.StopDebugServer(ctx)
	c.cleanup()
	c.cleanup = nil
	c.fs = nil
	return nil
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

func BuildFileSystem(ctx context.Context, cfg *config.Config, opts Options) (vfs.FileSystem, func(), error) {
	if err := config.Validate(cfg); err != nil {
		return nil, nil, err
	}
	limits, err := cfg.EffectiveBandwidthLimits()
	if err != nil {
		return nil, nil, err
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
	return osutil.ExpandHome("~/.qrypt/qrypt-cache")
}

func DefaultUploadDir() string {
	return osutil.ExpandHome("~/.qrypt/qrypt-upload")
}

func DefaultStateDir() string {
	return osutil.ExpandHome("~/.qrypt/qrypt-state")
}

func NewStorageLayout(cfg *config.Config, runtime RuntimeLayout) RuntimeLayout {
	storage := config.StorageConfig{}
	if cfg != nil {
		storage = cfg.Storage
	}
	readCacheDir := osutil.ExpandHome(storage.ReadCacheDir)
	if readCacheDir == "" {
		readCacheDir = filepath.Join(DefaultCacheDir(), "read")
	}
	thumbnailDir := osutil.ExpandHome(storage.ThumbnailCacheDir)
	if thumbnailDir == "" {
		thumbnailDir = filepath.Join(DefaultCacheDir(), "thumbnail")
	}
	uploadDir := osutil.ExpandHome(storage.UploadDir)
	if uploadDir == "" {
		uploadDir = DefaultUploadDir()
	}
	stateDir := osutil.ExpandHome(storage.StateDir)
	if stateDir == "" {
		stateDir = DefaultStateDir()
	}
	logDir := osutil.ExpandHome(storage.LogDir)
	tmpDir := osutil.ExpandHome(storage.TmpDir)
	layout := RuntimeLayout{
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
		base.RootDir = osutil.ExpandHome(override.RootDir)
	}
	if override.ConfigDir != "" {
		base.ConfigDir = osutil.ExpandHome(override.ConfigDir)
	}
	if override.ReadCacheDir != "" {
		base.ReadCacheDir = osutil.ExpandHome(override.ReadCacheDir)
	}
	if override.ThumbnailDir != "" {
		base.ThumbnailDir = osutil.ExpandHome(override.ThumbnailDir)
	}
	if override.UploadDir != "" {
		base.UploadDir = osutil.ExpandHome(override.UploadDir)
	}
	if override.StateDir != "" {
		base.StateDir = osutil.ExpandHome(override.StateDir)
	}
	if override.DriverDir != "" {
		base.DriverDir = osutil.ExpandHome(override.DriverDir)
	} else if override.StateDir != "" {
		base.DriverDir = filepath.Join(base.StateDir, "driver")
	}
	if override.LogDir != "" {
		base.LogDir = osutil.ExpandHome(override.LogDir)
	}
	if override.TmpDir != "" {
		base.TmpDir = osutil.ExpandHome(override.TmpDir)
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

func buildNamespace(ctx context.Context, cfg *config.Config, layout RuntimeLayout, limiter *drive.BandwidthLimiter, opts Options) (vfs.FileSystem, func(), error) {
	var mounts []vfs.Mount
	var drivers []drive.Driver
	for _, mountCfg := range cfg.Mounts {
		if opts.MountName != "" && mountCfg.Name != opts.MountName {
			continue
		}
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
			dropAll(ctx, drivers)
			return nil, nil, err
		}
		raw, err := drive.New(mountCfg.Type, params)
		if err != nil {
			dropAll(ctx, drivers)
			return nil, nil, err
		}
		installDriverStateStore(raw, stateDir)
		if err := raw.Init(ctx); err != nil {
			dropAll(ctx, append(drivers, raw))
			return nil, nil, err
		}
		rootID, err := resolveMountRootID(ctx, raw)
		if err != nil {
			dropAll(ctx, append(drivers, raw))
			return nil, nil, fmt.Errorf("config: mount %s resolve root: %w", mountCfg.Name, err)
		}
		drivers = append(drivers, raw)
		var drv drive.Driver = drive.WrapBandwidthLimitedDriver(raw, limiter)
		enc := cfg.EncryptionFor(mountCfg.Name)
		if enc.Password != "" {
			if err := enc.Validate(); err != nil {
				dropAll(ctx, drivers)
				return nil, nil, err
			}
			cp, err := crypt.NewRcloneCipherFromConfig(enc)
			if err != nil {
				dropAll(ctx, drivers)
				return nil, nil, err
			}
			drv = crypt.NewDriver(drv, cp, crypt.DriverOptions{ContentDedup: enc.ContentDedup})
		}
		maxBytes := readCache.MaxSizeBytes()
		if maxBytes == 0 {
			maxBytes = 512 << 20
		}
		uploadDelay, err := config.ParseDuration(upload.UploadDelay)
		if err != nil {
			dropAll(ctx, drivers)
			return nil, nil, fmt.Errorf("config: mount %s invalid upload.upload_delay: %w", mountCfg.Name, err)
		}
		deleteDelay, err := config.ParseDuration(upload.DeleteDelay)
		if err != nil {
			dropAll(ctx, drivers)
			return nil, nil, fmt.Errorf("config: mount %s invalid upload.delete_delay: %w", mountCfg.Name, err)
		}
		if upload.UploadWorkers < 0 {
			dropAll(ctx, drivers)
			return nil, nil, fmt.Errorf("config: mount %s invalid upload.upload_workers: must be non-negative", mountCfg.Name)
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
			dropAll(ctx, drivers)
			return nil, nil, err
		}
		mounts = append(mounts, vfs.Mount{Name: mountCfg.Name, FS: fs})
	}
	if len(mounts) == 0 {
		if opts.MountName != "" {
			return nil, nil, fmt.Errorf("config: mount %q not found", opts.MountName)
		}
		return nil, nil, fmt.Errorf("config: no mounts selected")
	}
	if opts.MountName != "" && !opts.ForceNamespace {
		fs := mounts[0].FS
		return fs, func() {
			flushReadCache(fs)
			dropAll(ctx, drivers)
		}, nil
	}
	ns, err := vfs.NewNamespace(mounts)
	if err != nil {
		dropAll(ctx, drivers)
		return nil, nil, err
	}
	return ns, func() {
		flushReadCache(ns)
		dropAll(ctx, drivers)
	}, nil
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

func initRuntimeLogger(cfg *config.Config, layout RuntimeLayout) error {
	if layout.LogDir == "" {
		return nil
	}
	level := "info"
	if cfg != nil && strings.TrimSpace(cfg.Logging.LogLevel) != "" {
		level = cfg.Logging.LogLevel
	}
	logFile := filepath.Join(layout.LogDir, "qrypt.log")
	errFile := filepath.Join(layout.LogDir, "qrypt-error.log")
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
