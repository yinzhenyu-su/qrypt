package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// cliMountConfig holds parsed FUSE mount options from config.
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

// addSingleDriveFlags registers --driver, --root, --password, --salt,
// --filename-encryption, and --filename-encoding as persistent flags.
func addSingleDriveFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&driverName, "driver", "localfs", "backend driver")
	cmd.PersistentFlags().StringVar(&root, "root", "", "backend root")
	cmd.PersistentFlags().StringVar(&password, "password", "", "rclone crypt password")
	cmd.PersistentFlags().StringVar(&salt, "salt", "", "rclone crypt salt")
	cmd.PersistentFlags().StringVar(&fileNameEncryption, "filename-encryption", "", "rclone crypt filename encryption: standard, off, obfuscate")
	cmd.PersistentFlags().StringVar(&fileNameEncoding, "filename-encoding", "", "rclone crypt filename encoding: base32, base64")
}

// addMountNameFlag registers --mount-name as a persistent flag.
func addMountNameFlag(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&mountName, "mount-name", "", "mount name used when reading config encryption")
}

// initLoggerFromConfig loads logging configuration from the TOML config file
// and initializes the global logger. This is called once at startup.
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

// mountPointFromConfig returns the effective mount point from the config.
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

// mountConfigFromConfig reads FUSE mount options from the config file.
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

// loggingFromConfig returns the logging config block from the TOML file.
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

// expandHome replaces a leading ~/ with the user's home directory.
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

// buildFileSystem constructs a vfs.FileSystem from the provided flags and
// configuration. When a config file with multiple mounts is given, a
// Namespace is created. Otherwise a single-drive VFS is returned.
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
		return nil, nil, fmt.Errorf("missing -root or config")
	}
	raw, err := drive.New(driverName, drive.Params{"root": root})
	if err != nil {
		return nil, nil, err
	}
	installDriverStateStore(raw, cacheDir)
	if err := raw.Init(ctx); err != nil {
		return nil, nil, err
	}

	raw = drive.WrapRateLimitedDriver(raw, bandwidthLimiterFromConfig(configPath))
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

// bandwidthLimiterFromConfig reads bandwidth limits from the config file.
func bandwidthLimiterFromConfig(configPath string) *drive.RateLimiter {
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

// bandwidthLimiter creates a RateLimiter from the given limits.
func bandwidthLimiter(limits config.BandwidthLimits) *drive.RateLimiter {
	return drive.NewRateLimiter(drive.RateLimits{
		DownloadBytesPerSecond: limits.DownloadBytesPerSecond,
		UploadBytesPerSecond:   limits.UploadBytesPerSecond,
	})
}

// effectiveCacheDir resolves the best cache directory, preferring the
// --cache flag override, then the config value, then the default.
func effectiveCacheDir(cmd *cobra.Command, cacheDir string, cfg *config.Config) string {
	if cmd.Flags().Changed("cache") {
		return expandHome(cacheDir)
	}
	if cfg != nil && cfg.CacheDir != "" {
		return expandHome(cfg.CacheDir)
	}
	if cacheDir != "" {
		return expandHome(cacheDir)
	}
	return defaultCacheDir()
}

// defaultCacheDir returns the default temporary cache directory.
func defaultCacheDir() string {
	return filepath.Join(os.TempDir(), "qrypt-cache")
}

// buildNamespace constructs a multi-mount Namespace from config, applying
// per-mount encryption, caching, and bandwidth limits.
func buildNamespace(ctx context.Context, cmd *cobra.Command, cfg *config.Config, cacheDir, mountName, password, salt, fileNameEncryption, fileNameEncoding string, limiter *drive.RateLimiter) (vfs.FileSystem, func(), error) {
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
		var drv drive.Driver = drive.WrapRateLimitedDriver(raw, limiter)
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

// installDriverStateStore installs a file-based state store on the driver
// if the driver implements StateStoreInstaller.
func installDriverStateStore(driver drive.Driver, cacheDir string) {
	if installer, ok := driver.(drive.StateStoreInstaller); ok {
		installer.InstallStateStore(drive.NewFileStateStore(filepath.Join(cacheDir, "driver")))
	}
}

// dropAll calls Drop on every driver in the slice, ignoring errors.
func dropAll(ctx context.Context, drivers []drive.Driver) {
	for _, drv := range drivers {
		_ = drv.Drop(ctx)
	}
}

// encryptionConfigFromFlags builds the encryption configuration from flags
// combined with any config file values.
func encryptionConfigFromFlags(cmd *cobra.Command, configPath, mountName, password, salt, fileNameEncryption, fileNameEncoding string) (crypt.Config, bool, error) {
	changed := map[string]bool{}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		changed[f.Name] = true
	})

	var enc crypt.Config
	if configPath != "" {
		cfg, err := config.Load(configPath)
		if err != nil {
			return crypt.Config{}, false, err
		}
		enc = cfg.EncryptionFor(mountName)
	}

	overrides := config.EncryptionOverrides{}
	if changed["password"] {
		overrides.Password = &password
	}
	if changed["salt"] {
		overrides.Salt = &salt
	}
	if changed["filename-encryption"] {
		overrides.FileNameEncryption = &fileNameEncryption
	}
	if changed["filename-encoding"] {
		overrides.FileNameEncoding = &fileNameEncoding
	}
	enc = config.ApplyEncryptionOverrides(enc, overrides)

	enabled := enc.Password != "" || changed["password"] || changed["salt"] || changed["filename-encryption"] || changed["filename-encoding"]
	if !enabled {
		return enc, false, nil
	}
	return enc, true, enc.Validate()
}

// encryptionConfigFromValues applies CLI flag overrides to an existing
// encryption config (e.g. from the config file).
func encryptionConfigFromValues(cmd *cobra.Command, base crypt.Config, password, salt, fileNameEncryption, fileNameEncoding string) (crypt.Config, bool, error) {
	changed := map[string]bool{}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		changed[f.Name] = true
	})
	overrides := config.EncryptionOverrides{}
	if changed["password"] {
		overrides.Password = &password
	}
	if changed["salt"] {
		overrides.Salt = &salt
	}
	if changed["filename-encryption"] {
		overrides.FileNameEncryption = &fileNameEncryption
	}
	if changed["filename-encoding"] {
		overrides.FileNameEncoding = &fileNameEncoding
	}
	enc := config.ApplyEncryptionOverrides(base, overrides)
	enabled := enc.Password != "" || changed["password"] || changed["salt"] || changed["filename-encryption"] || changed["filename-encoding"]
	if !enabled {
		return enc, false, nil
	}
	return enc, true, enc.Validate()
}

// put uploads a local file to the remote filesystem, waiting for completion.
func put(ctx context.Context, fs vfs.FileSystem, local, remote string) error {
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := fs.Create(ctx, remote); err != nil {
		return err
	}
	buf := make([]byte, 256*1024)
	var off int64
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			written, err := fs.WriteAt(ctx, remote, buf[:n], off)
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
	if err := fs.Flush(ctx, remote); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if len(fs.Pending()) == 0 {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("upload still pending: %s", remote)
}

func printPendingVerbose(w io.Writer, pending []vfs.PendingFile) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tSIZE\tLOCAL\tSTAGING\tRETRY\tLAST_ATTEMPT\tNEXT_ATTEMPT\tLAST_ERROR")
	for _, item := range pending {
		status, size := stagingStatus(item)
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d\t%s\t%s\t%s\n",
			item.Path,
			item.Size,
			item.LocalPath,
			formatStagingStatus(status, size),
			item.RetryCount,
			formatUnixNano(item.LastAttemptAt),
			formatUnixNano(item.NextAttemptAt),
			item.LastError,
		)
	}
	_ = tw.Flush()
}

func stagingStatus(item vfs.PendingFile) (string, int64) {
	if item.LocalPath == "" {
		return "missing", 0
	}
	info, err := os.Stat(item.LocalPath)
	if err != nil {
		return "missing", 0
	}
	if info.Size() != item.Size {
		return "size-mismatch", info.Size()
	}
	return "ok", info.Size()
}

func formatStagingStatus(status string, size int64) string {
	if status == "ok" {
		return "ok"
	}
	if status == "size-mismatch" {
		return fmt.Sprintf("size-mismatch(%d)", size)
	}
	return status
}

func formatUnixNano(value int64) string {
	if value == 0 {
		return "-"
	}
	return time.Unix(0, value).Format(time.RFC3339)
}
