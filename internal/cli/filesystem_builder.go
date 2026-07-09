package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func buildFileSystem(ctx context.Context, configPath string) (vfs.FileSystem, func(), error) {
	if configPath == "" {
		return nil, nil, configNotFoundError()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
	}
	return buildFileSystemFromConfig(ctx, cfg)
}

func buildFileSystemFromConfig(ctx context.Context, cfg *config.Config) (vfs.FileSystem, func(), error) {
	if cfg == nil {
		return nil, nil, configNotFoundError()
	}
	if err := validateConfig(cfg); err != nil {
		return nil, nil, err
	}
	limits, err := cfg.EffectiveBandwidthLimits()
	if err != nil {
		return nil, nil, err
	}
	return buildNamespace(ctx, cfg, effectiveCacheDir(cfg), bandwidthLimiter(limits))
}

func bandwidthLimiter(limits config.BandwidthLimits) *drive.BandwidthLimiter {
	return drive.NewBandwidthLimiter(drive.BandwidthLimits{
		DownloadBytesPerSecond: limits.DownloadBytesPerSecond,
		UploadBytesPerSecond:   limits.UploadBytesPerSecond,
	})
}

func effectiveCacheDir(cfg *config.Config) string {
	if cfg != nil && cfg.CacheDir != "" {
		return osutil.ExpandHome(cfg.CacheDir)
	}
	return defaultCacheDir()
}

func defaultCacheDir() string {
	return filepath.Join(os.TempDir(), "qrypt-cache")
}

func buildNamespace(ctx context.Context, cfg *config.Config, cacheDir string, limiter *drive.BandwidthLimiter) (vfs.FileSystem, func(), error) {
	var mounts []vfs.Mount
	var drivers []drive.Driver
	for _, mountCfg := range cfg.Mounts {
		if mountCfg.Name == "" {
			return nil, nil, fmt.Errorf("config: mount name required")
		}
		if mountCfg.Type == "" {
			return nil, nil, fmt.Errorf("config: mount %s missing type", mountCfg.Name)
		}
		params := drive.Params{}
		for key, value := range mountCfg.Params {
			params[key] = value
		}
		if cfg.Logging.LogLevel == "debug" || cfg.Logging.LogLevel == "" && logging.L != nil {
		}
		cache := cfg.CacheFor(mountCfg.Name)
		mountCacheDir := cache.Dir
		if mountCacheDir == "" {
			mountCacheDir = filepath.Join(cacheDir, mountCfg.Name)
		}
		raw, err := drive.New(mountCfg.Type, params)
		if err != nil {
			dropAll(ctx, drivers)
			return nil, nil, err
		}
		installDriverStateStore(raw, mountCacheDir)
		if err := raw.Init(ctx); err != nil {
			dropAll(ctx, append(drivers, raw))
			return nil, nil, err
		}
		drivers = append(drivers, raw)
		var drv drive.Driver = drive.WrapBandwidthLimitedDriver(raw, limiter)
		enc := cfg.EncryptionFor(mountCfg.Name)
		enabled := enc.Password != ""
		if enabled {
			if err := enc.Validate(); err != nil {
				dropAll(ctx, drivers)
				return nil, nil, err
			}
			cp, err := crypt.NewRcloneCipherFromConfig(enc)
			if err != nil {
				dropAll(ctx, drivers)
				return nil, nil, err
			}
			drv = crypt.NewDriver(drv, cp)
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
			CacheDir:      mountCacheDir,
			CacheMaxBytes: maxBytes,
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
		return nil, nil, fmt.Errorf("config: no mounts selected")
	}
	ns, err := vfs.NewNamespace(mounts)
	if err != nil {
		dropAll(ctx, drivers)
		return nil, nil, err
	}
	return ns, func() { dropAll(ctx, drivers) }, nil
}

func installDriverStateStore(driver drive.Driver, cacheDir string) {
	if installer, ok := driver.(drive.StateStoreInstaller); ok {
		installer.InstallStateStore(drive.NewFileStateStore(filepath.Join(cacheDir, "driver")))
	}
}

func dropAll(ctx context.Context, drivers []drive.Driver) {
	for _, drv := range drivers {
		_ = drv.Drop(ctx)
	}
}
