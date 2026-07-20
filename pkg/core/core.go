package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type Options struct {
	ConfigPath     string
	WorkDir        string
	MountName      string
	ForceNamespace bool
	ReadChunkLimit int
}

type Core struct {
	fs          vfs.FileSystem
	cleanup     func()
	workLayout  WorkLayout
	debugServer *control.Server
}

const DefaultReadChunkLimit = 4 << 20

type WorkLayout struct {
	RootDir   string
	ConfigDir string
	CacheDir  string
	StateDir  string
	LogDir    string
	TmpDir    string
}

func Open(ctx context.Context, opts Options) (*Core, error) {
	if opts.ConfigPath == "" {
		return nil, fmt.Errorf("core: config path required")
	}
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	layout := NewWorkLayout(opts.WorkDir)
	if err := ensureWorkLayout(layout); err != nil {
		return nil, err
	}
	if err := initWorkDirLogger(cfg, layout); err != nil {
		return nil, err
	}
	fs, cleanup, err := BuildFileSystem(ctx, cfg, opts)
	if err != nil {
		return nil, err
	}
	fs.Start(ctx)
	c := &Core{fs: fs, cleanup: cleanup, workLayout: layout}
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

func (c *Core) Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	if c == nil || c.fs == nil {
		return nil, fmt.Errorf("core: closed")
	}
	return c.fs.Read(ctx, path, offset, size)
}

func (c *Core) ReadAt(ctx context.Context, path string, offset int64, length int, limit int) ([]byte, error) {
	if offset < 0 {
		return nil, fmt.Errorf("core: offset must be non-negative")
	}
	if length < 0 {
		return nil, fmt.Errorf("core: length must be non-negative")
	}
	if length == 0 {
		return []byte{}, nil
	}
	if limit <= 0 {
		limit = DefaultReadChunkLimit
	}
	if length > limit {
		return nil, fmt.Errorf("core: read length %d exceeds limit %d", length, limit)
	}
	rc, err := c.Read(ctx, path, offset, int64(length))
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func (c *Core) DebugSnapshotJSON(ctx context.Context) (string, error) {
	if c == nil || c.fs == nil {
		return "", fmt.Errorf("core: closed")
	}
	snapshotter, ok := c.fs.(interface {
		DebugSnapshot() vfs.DebugSnapshot
	})
	if !ok {
		return "", fmt.Errorf("core: debug snapshot unavailable")
	}
	return marshalJSON(snapshotter.DebugSnapshot())
}

func (c *Core) FlushReadCache() error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	flusher, ok := c.fs.(interface {
		FlushReadCache() error
	})
	if !ok {
		return fmt.Errorf("core: read cache flush unavailable")
	}
	return flusher.FlushReadCache()
}

func (c *Core) StartDebugServer(ctx context.Context, listen string) error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	if c.debugServer != nil {
		return fmt.Errorf("core: debug server already started")
	}
	snapshotter, ok := c.fs.(control.Snapshotter)
	if !ok {
		return fmt.Errorf("core: debug server requires filesystem debug snapshots")
	}
	server, err := control.NewServer(listen, snapshotter)
	if err != nil {
		return err
	}
	if err := server.Start(ctx); err != nil {
		return err
	}
	c.debugServer = server
	return nil
}

func (c *Core) StopDebugServer(ctx context.Context) error {
	if c == nil || c.debugServer == nil {
		return nil
	}
	err := c.debugServer.Close(ctx)
	c.debugServer = nil
	return err
}

func (c *Core) Close(ctx context.Context) error {
	if c == nil || c.cleanup == nil {
		return nil
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
	return buildNamespace(ctx, cfg, workLayoutCacheDir(cfg, opts.WorkDir), bandwidthLimiter(limits), opts)
}

func bandwidthLimiter(limits config.BandwidthLimits) *drive.BandwidthLimiter {
	return drive.NewBandwidthLimiter(drive.BandwidthLimits{
		DownloadBytesPerSecond: limits.DownloadBytesPerSecond,
		UploadBytesPerSecond:   limits.UploadBytesPerSecond,
	})
}

func EffectiveCacheDir(cfg *config.Config, workDir string) string {
	return workLayoutCacheDir(cfg, workDir)
}

func workLayoutCacheDir(cfg *config.Config, workDir string) string {
	if workDir != "" {
		return NewWorkLayout(workDir).CacheDir
	}
	if cfg != nil && cfg.CacheDir != "" {
		return osutil.ExpandHome(cfg.CacheDir)
	}
	return DefaultCacheDir()
}

func DefaultCacheDir() string {
	return osutil.ExpandHome("~/.qrypt/qrypt-cache")
}

func NewWorkLayout(workDir string) WorkLayout {
	if workDir == "" {
		return WorkLayout{}
	}
	root := osutil.ExpandHome(workDir)
	return WorkLayout{
		RootDir:   root,
		ConfigDir: filepath.Join(root, "config"),
		CacheDir:  filepath.Join(root, "cache"),
		StateDir:  filepath.Join(root, "state"),
		LogDir:    filepath.Join(root, "logs"),
		TmpDir:    filepath.Join(root, "tmp"),
	}
}

func ensureWorkLayout(layout WorkLayout) error {
	for _, dir := range []string{layout.ConfigDir, layout.CacheDir, layout.StateDir, layout.LogDir, layout.TmpDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func buildNamespace(ctx context.Context, cfg *config.Config, cacheDir string, limiter *drive.BandwidthLimiter, opts Options) (vfs.FileSystem, func(), error) {
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
		cache := cfg.CacheFor(mountCfg.Name)
		mountCacheDir := cache.Dir
		if opts.WorkDir != "" {
			mountCacheDir = filepath.Join(cacheDir, mountCfg.Name)
		} else if mountCacheDir == "" {
			mountCacheDir = filepath.Join(cacheDir, mountCfg.Name)
		} else {
			mountCacheDir = osutil.ExpandHome(mountCacheDir)
		}
		stateDir := driverStateDir(opts.WorkDir, mountCfg.Name, mountCacheDir)
		if opts.WorkDir != "" {
			if err := os.MkdirAll(stateDir, 0o700); err != nil {
				dropAll(ctx, drivers)
				return nil, nil, err
			}
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
		maxBytes := cache.MaxSizeBytes()
		if maxBytes == 0 {
			maxBytes = 512 << 20
		}
		uploadDelay, err := config.ParseDuration(cache.UploadDelay)
		if err != nil {
			dropAll(ctx, drivers)
			return nil, nil, fmt.Errorf("config: mount %s invalid cache.upload_delay: %w", mountCfg.Name, err)
		}
		deleteDelay, err := config.ParseDuration(cache.DeleteDelay)
		if err != nil {
			dropAll(ctx, drivers)
			return nil, nil, fmt.Errorf("config: mount %s invalid cache.delete_delay: %w", mountCfg.Name, err)
		}
		if cache.UploadWorkers < 0 {
			dropAll(ctx, drivers)
			return nil, nil, fmt.Errorf("config: mount %s invalid cache.upload_workers: must be non-negative", mountCfg.Name)
		}
		fs, err := vfs.New(drv, vfs.Options{
			Name:          mountCfg.Name,
			CacheDir:      mountCacheDir,
			CacheMaxBytes: maxBytes,
			RootID:        rootID,
			UploadDelay:   uploadDelay,
			UploadWorkers: cache.UploadWorkers,
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

func driverStateDir(workDir, mountName, fallbackCacheDir string) string {
	if workDir == "" {
		return filepath.Join(fallbackCacheDir, "driver")
	}
	return filepath.Join(NewWorkLayout(workDir).StateDir, mountName, "driver")
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

func initWorkDirLogger(cfg *config.Config, layout WorkLayout) error {
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
		return fmt.Errorf("initialize workdir logging: %w", err)
	}
	logging.L = newLogger
	logging.L.Infof("[CORE] workdir logging initialized")
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
