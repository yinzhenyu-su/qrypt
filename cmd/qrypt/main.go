package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/yinzhenyu/qrypt/internal/config"
	_ "github.com/yinzhenyu/qrypt/internal/driver/localfs"
	_ "github.com/yinzhenyu/qrypt/internal/driver/quark"
	"github.com/yinzhenyu/qrypt/internal/mount"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runWithContext(ctx, args); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}

func runWithContext(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("qrypt", flag.ContinueOnError)
	driverName := flags.String("driver", "localfs", "backend driver")
	root := flags.String("root", "", "backend root")
	cacheDir := flags.String("cache", "", "cache directory")
	configPath := flags.String("config", "", "TOML config file")
	mountName := flags.String("mount-name", "", "mount name used when reading config encryption")
	password := flags.String("password", "", "rclone crypt password")
	salt := flags.String("salt", "", "rclone crypt salt")
	fileNameEncryption := flags.String("filename-encryption", "", "rclone crypt filename encryption: standard, off, obfuscate")
	fileNameEncoding := flags.String("filename-encoding", "", "rclone crypt filename encoding: base32, base64")
	if err := flags.Parse(args); err != nil {
		return err
	}
	rest := flags.Args()
	if len(rest) == 0 {
		return fmt.Errorf("usage: qrypt [flags] list|put|cat|pending ...")
	}

	fs, cleanup, err := buildFileSystem(ctx, flags, *driverName, *root, *cacheDir, *configPath, *mountName, *password, *salt, *fileNameEncryption, *fileNameEncoding)
	if err != nil {
		return err
	}
	defer cleanup()
	fs.Start(ctx)

	switch rest[0] {
	case "list", "ls":
		path := "/"
		if len(rest) > 1 {
			path = rest[1]
		}
		entries, err := fs.List(ctx, path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			kind := "file"
			if entry.IsDir {
				kind = "dir "
			}
			fmt.Printf("%s %10d %s\n", kind, entry.Size, entry.Name)
		}
	case "put":
		if len(rest) != 3 {
			return fmt.Errorf("usage: qrypt [flags] put LOCAL REMOTE")
		}
		return put(ctx, fs, rest[1], rest[2])
	case "cat":
		if len(rest) != 2 {
			return fmt.Errorf("usage: qrypt [flags] cat REMOTE")
		}
		rc, err := fs.Read(ctx, rest[1], 0, 0)
		if err != nil {
			return err
		}
		defer rc.Close()
		_, err = io.Copy(os.Stdout, rc)
		return err
	case "pending":
		for _, pending := range fs.Pending() {
			fmt.Printf("%s %d %s\n", pending.Path, pending.Size, pending.LocalPath)
		}
	case "mount":
		if len(rest) > 2 {
			return fmt.Errorf("usage: qrypt [flags] mount [MOUNTPOINT]")
		}
		mountPoint := ""
		if len(rest) == 2 {
			mountPoint = rest[1]
		} else {
			var err error
			mountPoint, err = mountPointFromConfig(*configPath)
			if err != nil {
				return err
			}
		}
		mountConfig, err := mountConfigFromConfig(*configPath)
		if err != nil {
			return err
		}
		_, err = mount.NewMounter().Mount(ctx, fs, mount.Options{
			MountPoint:    expandHome(mountPoint),
			VolumeName:    mountConfig.VolumeName,
			NoAppleDouble: mountConfig.NoAppleDouble,
			TotalSpace:    mountConfig.TotalSpace,
			FreeSpace:     mountConfig.FreeSpace,
			TraceEnabled:  mountConfig.Logging.FuseTrace,
			TraceFile:     expandHome(mountConfig.Logging.FuseTraceFile),
			Foreground:    true,
		})
		return err
	default:
		return fmt.Errorf("unknown command: %s", rest[0])
	}
	return nil
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
	VolumeName    string
	NoAppleDouble bool
	TotalSpace    int64
	FreeSpace     int64
	Logging       config.LoggingConfig
}

func mountConfigFromConfig(configPath string) (cliMountConfig, error) {
	mountConfig := cliMountConfig{
		VolumeName:    "Qrypt",
		NoAppleDouble: true,
	}
	if configPath == "" {
		return mountConfig, nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return mountConfig, err
	}
	mountConfig.VolumeName = cfg.EffectiveVolumeName()
	mountConfig.NoAppleDouble = cfg.EffectiveNoAppleDouble()
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

func buildFileSystem(ctx context.Context, flags *flag.FlagSet, driverName, root, cacheDir, configPath, mountName, password, salt, fileNameEncryption, fileNameEncoding string) (vfs.FileSystem, func(), error) {
	if configPath != "" {
		cfg, err := config.Load(configPath)
		if err != nil {
			return nil, nil, err
		}
		cacheDir = effectiveCacheDir(flags, cacheDir, cfg)
		if len(cfg.Mounts) > 0 {
			return buildNamespace(ctx, flags, cfg, cacheDir, mountName, password, salt, fileNameEncryption, fileNameEncoding)
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
	if err := raw.Init(ctx); err != nil {
		return nil, nil, err
	}

	var drv drive.Driver = raw
	encCfg, encEnabled, err := encryptionConfigFromFlags(flags, configPath, mountName, password, salt, fileNameEncryption, fileNameEncoding)
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

func effectiveCacheDir(flags *flag.FlagSet, cacheDir string, cfg *config.Config) string {
	if flagVisited(flags, "cache") {
		return cacheDir
	}
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

func flagVisited(flags *flag.FlagSet, name string) bool {
	visited := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			visited = true
		}
	})
	return visited
}

func buildNamespace(ctx context.Context, flags *flag.FlagSet, cfg *config.Config, cacheDir, mountName, password, salt, fileNameEncryption, fileNameEncoding string) (vfs.FileSystem, func(), error) {
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
		raw, err := drive.New(mountCfg.Type, drive.Params(mountCfg.Params))
		if err != nil {
			dropAll(ctx, drivers)
			return nil, nil, err
		}
		if err := raw.Init(ctx); err != nil {
			dropAll(ctx, append(drivers, raw))
			return nil, nil, err
		}
		drivers = append(drivers, raw)
		var drv drive.Driver = raw
		enc := cfg.EncryptionFor(mountCfg.Name)
		enc, enabled, err := encryptionConfigFromValues(flags, enc, password, salt, fileNameEncryption, fileNameEncoding)
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
			drv = crypt.NewDriver(raw, cp)
		}
		cache := cfg.CacheFor(mountCfg.Name)
		mountCacheDir := cache.Dir
		if mountCacheDir == "" {
			mountCacheDir = filepath.Join(cacheDir, mountCfg.Name)
		}
		maxBytes := cache.MaxSizeBytes
		if maxBytes == 0 {
			maxBytes = 512 << 20
		}
		fs, err := vfs.New(drv, vfs.Options{CacheDir: mountCacheDir, CacheMaxBytes: maxBytes})
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

func dropAll(ctx context.Context, drivers []drive.Driver) {
	for _, drv := range drivers {
		_ = drv.Drop(ctx)
	}
}

func encryptionConfigFromFlags(flags *flag.FlagSet, configPath, mountName, password, salt, fileNameEncryption, fileNameEncoding string) (crypt.Config, bool, error) {
	visited := map[string]bool{}
	flags.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
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
	if visited["password"] {
		overrides.Password = &password
	}
	if visited["salt"] {
		overrides.Salt = &salt
	}
	if visited["filename-encryption"] {
		overrides.FileNameEncryption = &fileNameEncryption
	}
	if visited["filename-encoding"] {
		overrides.FileNameEncoding = &fileNameEncoding
	}
	enc = config.ApplyEncryptionOverrides(enc, overrides)

	enabled := enc.Password != "" || visited["password"] || visited["salt"] || visited["filename-encryption"] || visited["filename-encoding"]
	if !enabled {
		return enc, false, nil
	}
	return enc, true, enc.Validate()
}

func encryptionConfigFromValues(flags *flag.FlagSet, base crypt.Config, password, salt, fileNameEncryption, fileNameEncoding string) (crypt.Config, bool, error) {
	visited := map[string]bool{}
	flags.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})
	overrides := config.EncryptionOverrides{}
	if visited["password"] {
		overrides.Password = &password
	}
	if visited["salt"] {
		overrides.Salt = &salt
	}
	if visited["filename-encryption"] {
		overrides.FileNameEncryption = &fileNameEncryption
	}
	if visited["filename-encoding"] {
		overrides.FileNameEncoding = &fileNameEncoding
	}
	enc := config.ApplyEncryptionOverrides(base, overrides)
	enabled := enc.Password != "" || visited["password"] || visited["salt"] || visited["filename-encryption"] || visited["filename-encoding"]
	if !enabled {
		return enc, false, nil
	}
	return enc, true, enc.Validate()
}

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
