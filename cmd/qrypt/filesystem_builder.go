package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func initLoggerFromConfig(configPath string) {
	if configPath == "" {
		return
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return
	}
	level := cfg.Logging.LogLevel
	logFile := osutil.ExpandHome(cfg.Logging.LogFile)
	errFile := osutil.ExpandHome(cfg.Logging.ErrorFile)
	if level == "" && logFile == "" && errFile == "" {
		return
	}
	if level == "" {
		level = "info"
	}
	newLogger, err := logging.New(level, logFile, errFile, nil)
	if err != nil {
		return
	}
	logging.L = newLogger
}

func initTimeFromConfig(ctx context.Context, configPath string) {
	cfg := timeutil.NTPConfig{Enabled: true}
	if configPath != "" {
		loaded, err := config.Load(configPath)
		if err == nil {
			cfg.Enabled = loaded.Time.EffectiveNTPEnabled()
			cfg.Servers = loaded.Time.NTPServers
			if timeout, err := config.ParseDuration(loaded.Time.NTPTimeout); err == nil {
				cfg.Timeout = timeout
			}
			if poll, err := config.ParseDuration(loaded.Time.NTPPollInterval); err == nil {
				cfg.PollInterval = poll
			}
		}
	}
	timeutil.StartNTP(ctx, cfg)
}

func mountPointFromConfig(configPath string) (string, error) {
	if configPath == "" {
		return "", fmt.Errorf("usage: qrypt [flags] mount MOUNTPOINT")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", err
	}
	if mountPoint := cfg.EffectiveMountPoint(); mountPoint != "" {
		return mountPoint, nil
	}
	return "", fmt.Errorf("config: no mount_point found")
}

type cliMountConfig struct {
	VolumeName         string
	ReadOnly           bool
	AllowOther         bool
	DefaultPermissions bool
	NoAppleDouble      bool
	NoAppleXattr       bool
	AttrTimeout        time.Duration
	AttrTimeoutSet     bool
	EntryTimeout       time.Duration
	EntryTimeoutSet    bool
	NegativeTimeout    time.Duration
	TotalSpace         int64
	FreeSpace          int64
	Logging            config.LoggingConfig
}

func mountConfigFromConfig(configPath string) (cliMountConfig, error) {
	mountConfig := cliMountConfig{
		VolumeName:      "Qrypt",
		NoAppleDouble:   true,
		AttrTimeout:     time.Second,
		EntryTimeout:    time.Second,
		NegativeTimeout: 0,
	}
	if configPath == "" {
		return mountConfig, nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return mountConfig, err
	}
	mountConfig.VolumeName = cfg.EffectiveVolumeName()
	mountConfig.ReadOnly = cfg.ReadOnly
	mountConfig.AllowOther = cfg.AllowOther
	mountConfig.DefaultPermissions = cfg.DefaultPermissions
	mountConfig.NoAppleDouble = cfg.EffectiveNoAppleDouble()
	mountConfig.NoAppleXattr = cfg.EffectiveNoAppleXattr()
	attrTimeout, err := config.ParseDuration(cfg.AttrTimeout)
	if err != nil {
		return mountConfig, fmt.Errorf("config: invalid attr_timeout: %w", err)
	}
	if strings.TrimSpace(cfg.AttrTimeout) != "" {
		mountConfig.AttrTimeout = attrTimeout
		mountConfig.AttrTimeoutSet = true
	}
	entryTimeout, err := config.ParseDuration(cfg.EntryTimeout)
	if err != nil {
		return mountConfig, fmt.Errorf("config: invalid entry_timeout: %w", err)
	}
	if strings.TrimSpace(cfg.EntryTimeout) != "" {
		mountConfig.EntryTimeout = entryTimeout
		mountConfig.EntryTimeoutSet = true
	}
	negativeTimeout, err := config.ParseDuration(cfg.NegativeTimeout)
	if err != nil {
		return mountConfig, fmt.Errorf("config: invalid negative_timeout: %w", err)
	}
	mountConfig.NegativeTimeout = negativeTimeout
	totalSpace, freeSpace, err := cfg.EffectiveSpaceBytes()
	if err != nil {
		return mountConfig, err
	}
	mountConfig.TotalSpace = totalSpace
	mountConfig.FreeSpace = freeSpace
	mountConfig.Logging = cfg.Logging
	return mountConfig, nil
}

func loggingFromConfig(configPath string) (config.LoggingConfig, error) {
	var logging config.LoggingConfig
	if configPath == "" {
		return logging, nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return logging, err
	}
	return cfg.Logging, nil
}

func buildFileSystem(ctx context.Context, configPath string) (vfs.FileSystem, func(), error) {
	if configPath == "" {
		return nil, nil, fmt.Errorf("missing --config")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
	}
	limits, err := cfg.EffectiveBandwidthLimits()
	if err != nil {
		return nil, nil, err
	}
	if len(cfg.Mounts) == 0 {
		return nil, nil, fmt.Errorf("config: at least one [[mounts]] entry is required")
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

func requireConfig() error {
	if configPath == "" {
		return fmt.Errorf("missing --config")
	}
	return nil
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

func put(ctx context.Context, fs vfs.FileSystem, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := fs.Create(ctx, remotePath); err != nil {
		return err
	}
	buf := make([]byte, 256*1024)
	var off int64
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			written, err := fs.WriteAt(ctx, remotePath, buf[:n], off)
			if err != nil {
				return err
			}
			off += int64(written)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := fs.Flush(ctx, remotePath); err != nil {
		return err
	}
	return nil
}
