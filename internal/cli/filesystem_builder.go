package cli

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/core"
)

func buildFileSystem(ctx context.Context, configPath string) (builtFS, func(), error) {
	if configPath == "" {
		return nil, nil, configNotFoundError()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
	}
	return buildFileSystemFromConfig(ctx, cfg)
}

func buildFileSystemFromConfig(ctx context.Context, cfg *config.Config) (builtFS, func(), error) {
	return buildFileSystemFromConfigMount(ctx, cfg, "")
}

// buildFileSystemFromConfigMount builds the filesystem as the mount command
// sees it: only the named mount, without the /MOUNT/ namespace prefix. Its
// root is the drive's root, which is what a persistent single-drive mount
// presents. One-shot fs commands deliberately take the opposite view: they
// build with ForceNamespace=true so paths keep the /MOUNT/ form (see
// openFileSystem).
func buildFileSystemFromConfigMount(ctx context.Context, cfg *config.Config, mountName string) (builtFS, func(), error) {
	return buildFileSystemFromConfigMountMode(ctx, cfg, mountName, false)
}

func buildFileSystemFromConfigMountMode(ctx context.Context, cfg *config.Config, mountName string, forceNamespace bool) (builtFS, func(), error) {
	return buildFileSystemWithBandwidth(ctx, cfg, mountName, forceNamespace, nil)
}

// buildFileSystemWithBandwidth builds a filesystem with an optional CLI
// bandwidth override (nil means use the config [bandwidth] section).
func buildFileSystemWithBandwidth(ctx context.Context, cfg *config.Config, mountName string, forceNamespace bool, bandwidth *config.BandwidthLimits) (builtFS, func(), error) {
	applyQryptHomeOverride(cfg)
	return core.BuildFileSystem(ctx, cfg, core.Options{
		MountName:      mountName,
		ForceNamespace: forceNamespace,
		Bandwidth:      bandwidth,
	})
}

func defaultCacheDir() string {
	return core.DefaultCacheDir()
}
