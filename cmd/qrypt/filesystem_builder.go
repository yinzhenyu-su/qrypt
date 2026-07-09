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

func initLoggerFromConfig(configPath string) error {
	if configPath == "" {
		return nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	level := cfg.Logging.LogLevel
	logFile := osutil.ExpandHome(cfg.Logging.LogFile)
	errFile := osutil.ExpandHome(cfg.Logging.ErrorFile)
	if level == "" && logFile == "" && errFile == "" {
		return nil
	}
	if level == "" {
		level = "info"
	}
	newLogger, err := logging.New(level, logFile, errFile, nil)
	if err != nil {
		return fmt.Errorf("initialize logging: %w", err)
	}
	logging.L = newLogger
	return nil
}

func initTimeFromConfig(ctx context.Context, configPath string) error {
	cfg := timeutil.NTPConfig{Enabled: true}
	if configPath != "" {
		loaded, err := config.Load(configPath)
		if err != nil {
			return err
		}
		cfg.Enabled = loaded.Time.EffectiveNTPEnabled()
		cfg.Servers = loaded.Time.NTPServers
		timeout, err := config.ParseDuration(loaded.Time.NTPTimeout)
		if err != nil {
			return fmt.Errorf("config: invalid time.ntp_timeout: %w", err)
		}
		cfg.Timeout = timeout
		poll, err := config.ParseDuration(loaded.Time.NTPPollInterval)
		if err != nil {
			return fmt.Errorf("config: invalid time.ntp_poll_interval: %w", err)
		}
		cfg.PollInterval = poll
	}
	timeutil.StartNTP(ctx, cfg)
	return nil
}

func mountPointFromConfig(configPath string) (string, error) {
	if configPath == "" {
		return "", configNotFoundError()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", err
	}
	if mountPoint := cfg.EffectiveMountPoint(); mountPoint != "" {
		return mountPoint, nil
	}
	return "", fmt.Errorf("mount point not specified (set mount_point in the config or pass MOUNTPOINT)")
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
		return nil, nil, configNotFoundError()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
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

func validateConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("config: configuration is empty")
	}
	if cfg.Version != "" && cfg.Version != "1" {
		return fmt.Errorf("config: unsupported version %q", cfg.Version)
	}
	if _, err := cfg.EffectiveBandwidthLimits(); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"attr_timeout":     cfg.AttrTimeout,
		"entry_timeout":    cfg.EntryTimeout,
		"negative_timeout": cfg.NegativeTimeout,
	} {
		if _, err := config.ParseDuration(value); err != nil {
			return fmt.Errorf("config: invalid %s: %w", name, err)
		}
	}
	if _, _, err := cfg.EffectiveSpaceBytes(); err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Logging.LogLevel)) {
	case "", "debug", "info", "warn", "warning", "error", "off", "none":
	default:
		return fmt.Errorf("config: invalid logging.log_level %q", cfg.Logging.LogLevel)
	}
	for name, value := range map[string]string{
		"time.ntp_timeout":       cfg.Time.NTPTimeout,
		"time.ntp_poll_interval": cfg.Time.NTPPollInterval,
	} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		duration, err := config.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("config: invalid %s: %w", name, err)
		}
		if duration <= 0 {
			return fmt.Errorf("config: %s must be greater than 0", name)
		}
	}
	if len(cfg.Mounts) == 0 {
		return fmt.Errorf("config: at least one [[mounts]] entry is required")
	}
	knownDrivers := make(map[string]bool)
	for _, name := range drive.Names() {
		knownDrivers[name] = true
	}
	seenMounts := make(map[string]bool)
	for index, mountCfg := range cfg.Mounts {
		label := fmt.Sprintf("mounts[%d]", index)
		if mountCfg.Name == "" {
			return fmt.Errorf("config: %s.name is required", label)
		}
		if mountCfg.Name == "." || mountCfg.Name == ".." || strings.ContainsAny(mountCfg.Name, `/\`) {
			return fmt.Errorf("config: %s.name %q must be a single path component", label, mountCfg.Name)
		}
		if seenMounts[mountCfg.Name] {
			return fmt.Errorf("config: duplicate mount name %q", mountCfg.Name)
		}
		seenMounts[mountCfg.Name] = true
		if !knownDrivers[mountCfg.Type] {
			return fmt.Errorf("config: mount %q has unknown driver %q", mountCfg.Name, mountCfg.Type)
		}
		for _, param := range drive.ParamSchema(mountCfg.Type) {
			if param.Required && strings.TrimSpace(mountCfg.Params[param.Name]) == "" {
				return fmt.Errorf("config: mount %q missing required parameter %q", mountCfg.Name, param.Name)
			}
		}
		params := drive.Params{}
		for key, value := range mountCfg.Params {
			params[key] = value
		}
		if _, err := drive.New(mountCfg.Type, params); err != nil {
			return fmt.Errorf("config: mount %q: %w", mountCfg.Name, err)
		}
		enc := cfg.EncryptionFor(mountCfg.Name)
		if enc.Password != "" {
			if err := enc.Validate(); err != nil {
				return fmt.Errorf("config: mount %q: %w", mountCfg.Name, err)
			}
		}
		cache := cfg.CacheFor(mountCfg.Name)
		if cache.MaxSize != "" {
			if _, err := config.ParseSize(cache.MaxSize); err != nil {
				return fmt.Errorf("config: mount %q invalid cache.max_size: %w", mountCfg.Name, err)
			}
		}
		if _, err := config.ParseDuration(cache.UploadDelay); err != nil {
			return fmt.Errorf("config: mount %q invalid cache.upload_delay: %w", mountCfg.Name, err)
		}
		if _, err := config.ParseDuration(cache.DeleteDelay); err != nil {
			return fmt.Errorf("config: mount %q invalid cache.delete_delay: %w", mountCfg.Name, err)
		}
		if cache.UploadWorkers < 0 {
			return fmt.Errorf("config: mount %q invalid cache.upload_workers: must be non-negative", mountCfg.Name)
		}
	}
	return nil
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

func requireConfig(configPath string) error {
	if configPath == "" {
		return configNotFoundError()
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
	return putReader(ctx, fs, f, remotePath)
}

func putReader(ctx context.Context, fs vfs.FileSystem, reader io.Reader, remotePath string) error {
	if err := fs.Create(ctx, remotePath); err != nil {
		return err
	}
	buf := make([]byte, 256*1024)
	var off int64
	for {
		n, readErr := reader.Read(buf)
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
