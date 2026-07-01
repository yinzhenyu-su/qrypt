package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
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
	logFile := expandHome(cfg.Logging.LogFile)
	errFile := expandHome(cfg.Logging.ErrorFile)
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

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func buildFileSystem(ctx context.Context, cmd *cobra.Command, driverName, root, cacheDir, configPath, mountName, password, salt, fileNameEncryption, fileNameEncoding string) (vfs.FileSystem, func(), error) {
	if configPath != "" {
		cfg, err := config.Load(configPath)
		if err != nil {
			return nil, nil, err
		}
		cacheDir = effectiveCacheDir(cmd, cacheDir, cfg)
		limits, err := cfg.EffectiveBandwidthLimits()
		if err != nil {
			return nil, nil, err
		}
		if len(cfg.Mounts) > 0 {
			return buildNamespace(ctx, cmd, cfg, cacheDir, mountName, password, salt, fileNameEncryption, fileNameEncoding, bandwidthLimiter(limits))
		}
	}
	if cacheDir == "" {
		cacheDir = defaultCacheDir()
	}
	if root == "" {
		return nil, nil, fmt.Errorf("missing -root")
	}
	raw, err := drive.New(driverName, drive.Params{"root": root})
	if err != nil {
		return nil, nil, err
	}
	installDriverStateStore(raw, cacheDir)
	if err := raw.Init(ctx); err != nil {
		return nil, nil, err
	}

	raw = drive.WrapBandwidthLimitedDriver(raw, bandwidthLimiterFromConfig(configPath))
	var drv drive.Driver = raw
	encCfg, encEnabled, err := encryptionConfigFromFlags(cmd, configPath, mountName, password, salt, fileNameEncryption, fileNameEncoding)
	if err != nil {
		raw.Drop(ctx)
		return nil, nil, err
	}
	if encEnabled {
		cp, err := crypt.NewRcloneCipherFromConfig(encCfg)
		if err != nil {
			raw.Drop(ctx)
			return nil, nil, err
		}
		drv = crypt.NewDriver(raw, cp)
	}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: cacheDir, CacheMaxBytes: 512 << 20})
	if err != nil {
		raw.Drop(ctx)
		return nil, nil, err
	}
	return fs, func() { _ = raw.Drop(ctx) }, nil
}

func bandwidthLimiterFromConfig(configPath string) *drive.BandwidthLimiter {
	if configPath == "" {
		return nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil
	}
	limits, err := cfg.EffectiveBandwidthLimits()
	if err != nil {
		return nil
	}
	return bandwidthLimiter(limits)
}

func bandwidthLimiter(limits config.BandwidthLimits) *drive.BandwidthLimiter {
	return drive.NewBandwidthLimiter(drive.BandwidthLimits{
		DownloadBytesPerSecond: limits.DownloadBytesPerSecond,
		UploadBytesPerSecond:   limits.UploadBytesPerSecond,
	})
}

func effectiveCacheDir(cmd *cobra.Command, cacheDir string, cfg *config.Config) string {
	if cfg != nil && cfg.CacheDir != "" {
		return expandHome(cfg.CacheDir)
	}
	if cacheDir != "" {
		return cacheDir
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

func buildNamespace(ctx context.Context, cmd *cobra.Command, cfg *config.Config, cacheDir, mountName, password, salt, fileNameEncryption, fileNameEncoding string, limiter *drive.BandwidthLimiter) (vfs.FileSystem, func(), error) {
	var mounts []vfs.Mount
	var drivers []drive.Driver
	for _, mountCfg := range cfg.Mounts {
		if mountName != "" && mountCfg.Name != mountName {
			continue
		}
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
		enc, enabled, err := encryptionConfigFromValues(cmd, enc, password, salt, fileNameEncryption, fileNameEncoding)
		if err != nil {
			dropAll(ctx, drivers)
			return nil, nil, err
		}
		if enabled {
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

func encryptionConfigFromFlags(cmd *cobra.Command, configPath, mountName, password, salt, fileNameEncryption, fileNameEncoding string) (crypt.Config, bool, error) {
	changed := func(name string) bool { return cmd.PersistentFlags().Changed(name) }

	var enc crypt.Config
	if configPath != "" {
		cfg, err := config.Load(configPath)
		if err != nil {
			return crypt.Config{}, false, err
		}
		enc = cfg.EncryptionFor(mountName)
	}

	overrides := config.EncryptionOverrides{}
	if changed("password") {
		overrides.Password = &password
	}
	if changed("salt") {
		overrides.Salt = &salt
	}
	if changed("filename-encryption") {
		overrides.FileNameEncryption = &fileNameEncryption
	}
	if changed("filename-encoding") {
		overrides.FileNameEncoding = &fileNameEncoding
	}
	enc = config.ApplyEncryptionOverrides(enc, overrides)

	enabled := enc.Password != "" || changed("password") || changed("salt") || changed("filename-encryption") || changed("filename-encoding")
	if !enabled {
		return enc, false, nil
	}
	return enc, true, enc.Validate()
}

func encryptionConfigFromValues(cmd *cobra.Command, base crypt.Config, password, salt, fileNameEncryption, fileNameEncoding string) (crypt.Config, bool, error) {
	changed := func(name string) bool { return cmd.PersistentFlags().Changed(name) }

	overrides := config.EncryptionOverrides{}
	if changed("password") {
		overrides.Password = &password
	}
	if changed("salt") {
		overrides.Salt = &salt
	}
	if changed("filename-encryption") {
		overrides.FileNameEncryption = &fileNameEncryption
	}
	if changed("filename-encoding") {
		overrides.FileNameEncoding = &fileNameEncoding
	}
	enc := config.ApplyEncryptionOverrides(base, overrides)
	enabled := enc.Password != "" || changed("password") || changed("salt") || changed("filename-encryption") || changed("filename-encoding")
	if !enabled {
		return enc, false, nil
	}
	return enc, true, enc.Validate()
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
